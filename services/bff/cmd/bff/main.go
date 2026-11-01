// Servidor del canal del titular (BFF), incluido el lado humano del modelo B.
//
// Existe por el sandbox. El modelo B necesita DOS canales vivos —la plataforma
// pide el permiso por la API de socio y una persona lo concede aquí— y sin este
// binario el único sitio donde el flujo completo corría era una prueba de Go.
// Un socio no puede integrar contra una prueba.
//
// Variables de entorno:
//
//	DATABASE_URL    PostgreSQL. Habilita las pantallas de permiso del modelo B.
//	CORE_ADDR       core gRPC (por defecto 127.0.0.1:50051)
//	BIND_ADDR       dirección de escucha (por defecto 127.0.0.1:8080)
//	ALLOW_DEV_AUTH  "true" monta la autenticación de juguete, /dev y el alta con
//	                KYC simulado. SOLO sandbox.
//	                Con ella el canal autentica de verdad: teléfonos vinculados
//	                (llave en el chip seguro + cara o clave) y, con
//	                PASSKEY_RP_ID, passkeys.
//	PASSKEY_RP_ID       dominio al que quedan atadas las passkeys.
//	PASSKEY_RP_ORIGINS  orígenes aceptados, separados por coma (https://<RP_ID>,
//	                    android:apk-key-hash:<hash>).
//	PASSKEY_RP_NAME     nombre que muestra el sistema operativo. Por defecto AIBank.
//	RISK_URL            motor de riesgo (risk/, aibank_risk.server). Con él, tarjeta
//	                    de crédito.
//	CORS_ALLOWED_ORIGINS  orígenes del portal web, separados por coma. Vacío = sin CORS.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// La zona de Lima viaja dentro del binario: la imagen del contenedor no trae
	// la base de zonas horarias, y sin ella la previsión no sabe qué día es hoy.
	_ "time/tzdata"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/agents"
	"github.com/aibank/aibank/services/aliases"
	"github.com/aibank/aibank/services/bff"
	"github.com/aibank/aibank/services/bff/sim"
	"github.com/aibank/aibank/services/control"
	"github.com/aibank/aibank/services/credit"
	"github.com/aibank/aibank/services/forecast"
	"github.com/aibank/aibank/services/oauth"
	"github.com/aibank/aibank/services/onboarding"
	"github.com/aibank/aibank/services/pockets"
	"github.com/aibank/aibank/services/stepup"
	_ "github.com/lib/pq"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("el servicio terminó con error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	devAuth := os.Getenv("ALLOW_DEV_AUTH") == "true"
	dsn := os.Getenv("DATABASE_URL")
	rpID := os.Getenv("PASSKEY_RP_ID")

	// Sin una forma de autenticar, el canal no arranca. Los teléfonos vinculados
	// y las passkeys son la forma real, y viven en la base; el sustituto de
	// desarrollo hay que pedirlo explícitamente, y
	// solo vale para el sandbox. Un servicio que se queda con el autenticador de
	// juguete porque nadie configuró el de verdad es exactamente el accidente que
	// este error impide.
	if rpID != "" && dsn == "" {
		return errors.New("PASSKEY_RP_ID necesita DATABASE_URL: las passkeys y las sesiones se guardan ahí")
	}
	if !devAuth && dsn == "" {
		return errors.New(
			"sin forma de autenticar: configura DATABASE_URL (teléfonos vinculados y, con " +
				"PASSKEY_RP_ID, passkeys) o, SOLO en el sandbox, ALLOW_DEV_AUTH=true")
	}
	if devAuth {
		log.Warn("AUTENTICACIÓN DE DESARROLLO ACTIVA: cualquiera puede emitir una sesión")
	}

	core, err := coreclient.Dial(ctx, env("CORE_ADDR", "127.0.0.1:50051"))
	if err != nil {
		return err
	}
	defer core.Close()

	var (
		chain   bff.ChainAuthenticator
		simAuth *sim.Authenticator
		setup   []func(*bff.Server) *bff.Server
	)

	if dsn != "" {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return err
		}
		defer db.Close()

		store := oauth.NewStore(db)
		if err := store.Migrate(ctx); err != nil {
			return err
		}
		setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithConsents(store) })

		agentStore := agents.NewStore(db)
		if err := agentStore.Migrate(ctx); err != nil {
			return err
		}
		setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithAgents(agentStore) })

		onboardingStore := onboarding.NewStore(db)
		if err := onboardingStore.Migrate(ctx); err != nil {
			return err
		}

		sessionStore, deviceSvc, err := setupDevices(ctx, db, onboardingStore)
		if err != nil {
			return err
		}
		chain = append(chain, bff.NewSessionAuthenticator(sessionStore))
		payees := stepup.NewService(db, stepup.DefaultPolicy())
		if err := payees.Migrate(ctx); err != nil {
			return err
		}
		ctrl := control.NewService(db, payees)
		if err := ctrl.Migrate(ctx); err != nil {
			return err
		}
		setup = append(setup, func(s *bff.Server) *bff.Server {
			return s.WithSessions(sessionStore).WithDevices(deviceSvc).WithControl(ctrl, payees, deviceSvc)
		})
		log.Info("teléfonos vinculados y capa de control habilitados")

		// El directorio de celulares necesita mandar SMS para verificar números.
		// Sin proveedor de SMS, solo el sandbox lo tiene, con códigos visibles.
		if devAuth {
			sender := aliases.NewSandboxSender()
			directory := aliases.NewService(db, sender)
			if err := directory.Migrate(ctx); err != nil {
				return err
			}
			setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithAliases(directory, sender) })
			log.Info("directorio de celulares habilitado con SMS SIMULADO: el código vuelve en la respuesta")
		} else {
			log.Warn("directorio de celulares deshabilitado: no hay proveedor de SMS")
		}

		// Bolsillos: dinero apartado dentro de la cuenta.
		pocketSvc := pockets.NewService(db, core)
		if err := pocketSvc.Migrate(ctx); err != nil {
			return err
		}
		setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithPockets(pocketSvc) })

		// Tarjeta de crédito: necesita el motor de riesgo, que decide. Sin él no se
		// origina nada.
		if riskURL := os.Getenv("RISK_URL"); riskURL != "" {
			risk := credit.NewHTTPAssessor(riskURL)
			cards := credit.NewService(db, core, risk, credit.Config{
				// Solo en el sandbox: una cuenta recién abierta nunca califica.
				SyntheticHistory: devAuth,
				Cooldown:         creditCooldown(devAuth),
			})
			if err := cards.Migrate(ctx); err != nil {
				return err
			}
			setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithCredit(cards) })
			log.Info("tarjeta de crédito habilitada", "motor_de_riesgo", riskURL, "historial_sintetico", devAuth)

			// La previsión de saldo lee del mismo motor el próximo ingreso, con el
			// mismo criterio de ingreso recurrente que la tarjeta.
			zone, err := time.LoadLocation(bff.DefaultTimeZone)
			if err != nil {
				return err
			}
			forecasts := forecast.NewService(core, cards, risk, zone).WithCardReserve(pocketSvc)
			setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithForecast(forecasts) })
		} else {
			log.Warn("tarjeta de crédito deshabilitada: falta RISK_URL")
		}

		if rpID != "" {
			pk, err := setupPasskeys(ctx, db, onboardingStore, sessionStore, rpID)
			if err != nil {
				return err
			}
			setup = append(setup, func(s *bff.Server) *bff.Server { return s.WithPasskeys(pk) })
			log.Info("passkeys habilitadas", "rp_id", rpID)
		}

		// El alta usa el simulador de KYC, que aprueba a casi todo el mundo. Fuera
		// del sandbox eso sería abrir cuentas sin verificar la identidad de nadie:
		// el alta solo se monta en modo desarrollo, hasta que haya un proveedor
		// de identidad real.
		if devAuth {
			svc, _, product, err := setupOnboarding(ctx, db, core, "USD")
			if err != nil {
				return err
			}
			setup = append(setup, func(s *bff.Server) *bff.Server {
				return s.WithOnboarding(svc, onboardingStore, product)
			})
			log.Info("alta de clientes habilitada con KYC SIMULADO", "producto", product,
				"dni_de_prueba", TestDNIRejected+" rechazo, "+TestDNIInconclusive+" revisión, "+
					TestDNIWatchlist+" listas")
		} else {
			log.Warn("alta de clientes deshabilitada: no hay proveedor de identidad real")
		}
	} else {
		log.Warn("sin DATABASE_URL: el canal arranca sin permisos del modelo B ni alta de clientes")
	}

	if devAuth {
		simAuth = sim.NewAuthenticator()
		chain = append(chain, simAuth)
		// Saldo de prueba desde la app. Fuera del sandbox la ruta no existe.
		setup = append(setup, func(s *bff.Server) *bff.Server {
			return s.WithSandboxFunds(sandboxCashPrefix, holderProductPrefix)
		})
	}

	server := bff.NewServer(core, chain, log)
	for _, apply := range setup {
		server = apply(server)
	}

	mux := http.NewServeMux()
	mux.Handle("/", server.Handler())
	if devAuth {
		mountDevRoutes(mux, core, simAuth, log)
	}

	addr := env("BIND_ADDR", "127.0.0.1:8080")
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux, parseOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("canal del titular escuchando", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// creditCooldown: tras una solicitud no aprobada, cuánto esperar para volver a
// evaluar. En el sandbox casi nada, para poder probar otra vez.
func creditCooldown(sandbox bool) time.Duration {
	if sandbox {
		return time.Minute
	}
	return 30 * 24 * time.Hour
}
