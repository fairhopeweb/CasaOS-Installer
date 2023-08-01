package route

import (
	"context"
	"net/http"

	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"github.com/IceWhaleTech/CasaOS-Installer/codegen"
	"github.com/IceWhaleTech/CasaOS-Installer/common"
	"github.com/IceWhaleTech/CasaOS-Installer/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

func (a *api) GetRelease(ctx echo.Context, params codegen.GetReleaseParams) error {
	tag := common.MainTag
	if params.Version != nil && *params.Version != "latest" {
		tag = *params.Version
	}

	release, err := service.GetRelease(ctx.Request().Context(), tag)
	if err != nil {
		message := err.Error()

		if err == service.ErrReleaseNotFound {
			return ctx.JSON(http.StatusNotFound, &codegen.ResponseNotFound{
				Message: &message,
			})
		}

		return ctx.JSON(http.StatusInternalServerError, &codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	upgradable := service.IsUpgradable(*release, "")

	return ctx.JSON(http.StatusOK, &codegen.ReleaseOK{
		Data:       release,
		Upgradable: &upgradable,
	})
}

func (a *api) InstallRelease(ctx echo.Context, params codegen.InstallReleaseParams) error {
	tag := "dev-test"
	if params.Version != nil && *params.Version != "latest" {
		tag = *params.Version
	}

	release, err := service.GetRelease(ctx.Request().Context(), tag)
	if err != nil {
		message := err.Error()

		if err == service.ErrReleaseNotFound {
			return ctx.JSON(http.StatusNotFound, &codegen.ResponseNotFound{
				Message: &message,
			})
		}

		return ctx.JSON(http.StatusInternalServerError, &codegen.ResponseInternalServerError{
			Message: &message,
		})
	}

	if release == nil {
		message := "release not found"
		return ctx.JSON(http.StatusNotFound, &codegen.ResponseNotFound{
			Message: &message,
		})
	}

	go func() {
		backgroundCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sysRoot := "/"

		if _, err := service.VerifyRelease(*release); err != nil {
			logger.Error("error while release verification: %s", zap.Error(err))
			return
		}

		if err := service.ExtractReleasePackages(service.ReleaseFilePath, *release); err != nil {
			logger.Error("error while extract release packages: %s", zap.Error(err))
			return
		}

		if err := service.ExtractReleasePackages(service.ReleaseFilePath+"/linux*", *release); err != nil {
			logger.Error("error while extract modules packages: %s", zap.Error(err))
			return
		}

		if err := service.InstallRelease(backgroundCtx, *release, sysRoot); err != nil {
			logger.Error("error while installing release", zap.Error(err))
			return
		}

		if err := service.ExecuteModuleInstallScript(service.ReleaseFilePath, *release); err != nil {
			logger.Error("error while install modules: %s", zap.Error(err))
			return
		}

		if err := service.SetStartUpAndLaunchModule(*release); err != nil {
			logger.Error("error while enable services: %s", zap.Error(err))
			return
		}

		if _, err = service.DownloadUninstallScript(backgroundCtx, sysRoot); err != nil {
			logger.Error("Downloading uninstall script: %s", zap.Error(err))
			return
		}

		if present := service.VerifyUninstallScript(); !present {
			logger.Error("uninstall script not found")
			return
		}
	}()

	message := "release being installed asynchronously"
	return ctx.JSON(http.StatusOK, &codegen.ResponseOK{
		Message: &message,
	})
}