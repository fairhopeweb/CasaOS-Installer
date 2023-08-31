//go:generate bash -c "mkdir -p codegen && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,server,spec -package codegen api/openapi.yaml > codegen/api.go"
//go:generate bash -c "mkdir -p codegen/message_bus && go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.12.4 -generate types,client -package message_bus https://raw.githubusercontent.com/IceWhaleTech/CasaOS-MessageBus/main/api/message_bus/openapi.yaml > codegen/message_bus/api.go"

package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/IceWhaleTech/CasaOS-Common/model"
	"github.com/IceWhaleTech/CasaOS-Common/utils/constants"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"

	util_http "github.com/IceWhaleTech/CasaOS-Common/utils/http"
	"github.com/IceWhaleTech/CasaOS-Installer/common"
	"github.com/IceWhaleTech/CasaOS-Installer/internal/config"
	"github.com/IceWhaleTech/CasaOS-Installer/route"
	"github.com/IceWhaleTech/CasaOS-Installer/service"
	"github.com/coreos/go-systemd/daemon"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

var (
	commit = "private build"
	date   = "private build"

	//go:embed api/index.html
	_docHTML string

	//go:embed api/openapi.yaml
	_docYAML string
	sysRoot  = "/"

	//go:embed build/sysroot/etc/casaos/installer.conf.sample
	_confSample string
)

func main() {
	service.InstallerService = service.NewInstallerService(sysRoot)

	service.UpdateStatusWithMessage(service.Idle, "up-to-date")

	go service.StartFallbackWebsite()
	// create config
	{
		// create default config file if not exist
		ConfigFilePath := filepath.Join(constants.DefaultConfigPath, common.InstallerName+"."+common.InstallerConfigType)
		if _, err := os.Stat(ConfigFilePath); os.IsNotExist(err) {
			fmt.Println("config file not exist, create it")
			// create config file
			file, err := os.Create(ConfigFilePath)
			if err != nil {
				panic(err)
			}
			defer file.Close()

			// write default config
			_, err = file.WriteString(_confSample)
			if err != nil {
				panic(err)
			}
		}
	}

	// parse arguments and intialize
	{
		configFlag := flag.String("c", "", "config file path")
		versionFlag := flag.Bool("v", false, "version")

		flag.Parse()

		if *versionFlag {
			fmt.Printf("v%s\n", common.InstallerVersion)
			os.Exit(0)
		}

		println("git commit:", commit)
		println("build date:", date)

		config.InitSetup(*configFlag)

		logger.LogInit(config.AppInfo.LogPath, config.AppInfo.LogSaveName, config.AppInfo.LogFileExt)

		service.MyService = service.NewService(config.CommonInfo.RuntimePath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// start migration.
	// only ZIMA OS should do this.
	// if it is CasaOS. the migration will be done by the installer. and will skip this.
	go service.StopFallbackWebsite()

	// err := service.StartMigration(sysRoot)
	err := service.InstallerService.MigrationInLaunch(sysRoot)
	if err != nil {
		logger.Error("error when trying to start migration", zap.Error(err))
	}

	{
		crontab := cron.New(cron.WithSeconds())

		go cronjob(ctx) // run once immediately

		if _, err := crontab.AddFunc("@every 10m", func() { cronjob(ctx) }); err != nil {
			panic(err)
		}

		// every 10 seconds for debug
		if _, err := crontab.AddFunc("@every 1s", func() {
		}); err != nil {
			panic(err)
		}

		crontab.Start()
		defer crontab.Stop()
	}

	go func() {
		for {
			// register at message bus
			if messageBus, err := service.MyService.MessageBus(); err != nil {
				logger.Info("error when trying to connect to message bus... skipping", zap.Error(err))
			} else {
				response, err := messageBus.RegisterEventTypesWithResponse(ctx, common.EventTypes)
				if err != nil {
					logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.Error(err))
				}

				if response != nil && response.StatusCode() != http.StatusOK {
					logger.Error("error when trying to register one or more event types - some event type will not be discoverable", zap.String("status", response.Status()), zap.String("body", string(response.Body)))
				}
				break
			}
		}
		time.Sleep(10 * time.Second)
	}()

	// initialize routers and register at gateway
	listener, err := net.Listen("tcp", net.JoinHostPort(common.Localhost, "0"))
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			// initialize routers and register at gateway
			if gateway, err := service.MyService.Gateway(); err != nil {
				logger.Info("error when trying to connect to gateway... skipping", zap.Error(err))
			} else {
				apiPaths := []string{
					route.V2APIPath,
					route.V2DocPath,
				}

				for _, apiPath := range apiPaths {
					if err := gateway.CreateRoute(&model.Route{
						Path:   apiPath,
						Target: "http://" + listener.Addr().String(),
					}); err != nil {
						panic(err)
					}
				}
				fmt.Println("gateway register success")
				break
			}
			time.Sleep(10 * time.Second)
		}
	}()

	mux := &util_http.HandlerMultiplexer{
		HandlerMap: map[string]http.Handler{
			"v2":  route.InitV2Router(),
			"doc": route.InitV2DocRouter(_docHTML, _docYAML),
		},
	}

	// notify systemd that we are ready
	{
		if supported, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
			logger.Error("Failed to notify systemd that installer service is ready", zap.Any("error", err))
		} else if supported {
			logger.Info("Notified systemd that installer service is ready")
		} else {
			logger.Info("This process is not running as a systemd service.")
		}
	}

	s := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // fix G112: Potential slowloris attack (see https://github.com/securego/gosec)
	}

	logger.Info("installer service is listening...", zap.String("address", listener.Addr().String()))
	if err := s.Serve(listener); err != nil {
		panic(err)
	}

}

func cronjob(ctx context.Context) {

	// TODO 考虑一下这个packageStatus的问题
	go service.UpdateStatusWithMessage(service.FetchUpdateBegin, "间隔触发更新")

	// release, err := service.GetRelease(ctx, service.GetReleaseBranch(sysRoot))
	release, err := service.InstallerService.GetRelease(ctx, service.GetReleaseBranch(sysRoot))

	if err != nil {
		logger.Error("error when trying to get release", zap.Error(err))
		return
	}

	if !service.ShouldUpgrade(*release, sysRoot) {
		service.UpdateStatusWithMessage(service.FetchUpdateEnd, "up-to-date")
		logger.Info("no need to upgrade", zap.String("latest version", release.Version))
		return
	} else {
		if service.IsUpgradable(*release, sysRoot) {
			service.UpdateStatusWithMessage(service.FetchUpdateEnd, "ready-to-update")
		} else {
			service.UpdateStatusWithMessage(service.FetchUpdateEnd, "out-of-date")
		}
	}

	// cache release packages if not already cached
	if _, err := service.VerifyRelease(*release); err != nil {
		// TODO 考虑一下这个packageStatus的问题

		go service.UpdateStatusWithMessage(service.DownloadBegin, "自动触发的下载")
		defer service.UpdateStatusWithMessage(service.DownloadEnd, "ready-to-update")

		logger.Info("error while verifying release - continue to download", zap.Error(err))

		// releaseFilePath, err := service.DownloadRelease(ctx, *release, true)
		releaseFilePath, err := service.InstallerService.DownloadRelease(ctx, *release, true)

		if err != nil {
			logger.Error("error when trying to download release", zap.Error(err))
			return
		}
		logger.Info("downloaded release", zap.String("release file path", releaseFilePath))
	}

	// TOOD disable migration when rauc install temporarily
	// // cache migration tools if not already cached
	// {
	// 	if service.VerifyAllMigrationTools(*release, sysRoot) {
	// 		logger.Info("all migration tools exist", zap.String("version", release.Version))
	// 		return
	// 	}

	// 	if _, err := service.DownloadAllMigrationTools(ctx, *release, sysRoot); err != nil {
	// 		logger.Error("error when trying to download migration tools", zap.Error(err))
	// 		return
	// 	}
	// }
}
