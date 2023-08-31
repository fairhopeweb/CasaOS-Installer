package route

import (
	"context"
	"net/http"

	"github.com/IceWhaleTech/CasaOS-Common/utils"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"github.com/IceWhaleTech/CasaOS-Installer/codegen"
	"github.com/IceWhaleTech/CasaOS-Installer/common"
	"github.com/IceWhaleTech/CasaOS-Installer/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

var sysRoot = "/"

func (a *api) GetStatus(ctx echo.Context) error {
	status, packageStatus := service.GetStatus()
	return ctx.JSON(http.StatusOK, &codegen.StatusOK{
		Data:    &status,
		Message: utils.Ptr(packageStatus),
	})
}

func (a *api) GetRelease(ctx echo.Context, params codegen.GetReleaseParams) error {

	// TODO 考虑一下这个packageStatus的问题
	go service.UpdateStatusWithMessage(service.FetchUpdateBegin, "主动触发的获取信息")
	tag := service.GetReleaseBranch(sysRoot)

	if params.Version != nil && *params.Version != "latest" {
		tag = *params.Version
	}

	release, err := service.GetRelease(ctx.Request().Context(), tag)
	if err != nil {
		message := err.Error()
		service.PublishEventWrapper(context.Background(), common.EventTypeCheckUpdateError, map[string]string{
			common.PropertyTypeMessage.Name: err.Error(),
		})

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
	if service.ShouldUpgrade(*release, sysRoot) {
		if upgradable {
			service.UpdateStatusWithMessage(service.FetchUpdateEnd, "ready-to-update")
		} else {
			service.UpdateStatusWithMessage(service.FetchUpdateEnd, "out-of-date")
		}

	} else {
		service.UpdateStatusWithMessage(service.FetchUpdateEnd, "up-to-date")
	}

	return ctx.JSON(http.StatusOK, &codegen.ReleaseOK{
		Data:       release,
		Upgradable: &upgradable,
	})
}

func (a *api) InstallRelease(ctx echo.Context, params codegen.InstallReleaseParams) error {
	go service.UpdateStatusWithMessage(service.InstallBegin, "主动触发的安装更新1级")
	defer service.UpdateStatusWithMessage(service.InstallEnd, "更新完成")

	tag := service.GetReleaseBranch(sysRoot)

	if params.Version != nil && *params.Version != "latest" {
		tag = *params.Version
	}

	release, err := service.InstallerService.GetRelease(ctx.Request().Context(), tag)
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
		// backgroundCtx, cancel := context.WithCancel(context.Background())
		// defer cancel()
		sysRoot := "/"

		// if the err is not nil. It mean should to download
		contentCtx := context.Background()

		go service.UpdateStatusWithMessage(service.DownloadBegin, "安装触发的下载")

		releasePath, err := service.InstallerService.DownloadRelease(contentCtx, *release, false)
		if err != nil {
		}

		service.UpdateStatusWithMessage(service.DownloadEnd, "ready-to-update")

		// TODO disable migration when rauc install temporarily
		// // to download migration script
		// if _, err := service.DownloadAllMigrationTools(backgroundCtx, *release, sysRoot); err != nil {
		// 	go service.PublishEventWrapper(context.Background(), common.EventTypeInstallUpdateError, map[string]string{
		// 		common.PropertyTypeMessage.Name: err.Error(),
		// 	})

		// 	logger.Error("error while download migration: %s", zap.Error(err))
		// 	// 回头这里重做一下，有rauc自己的migration
		// 	//return
		// }

		err = service.InstallerService.ExtractRelease(releasePath, *release)
		if err != nil {
			go service.PublishEventWrapper(context.Background(), common.EventTypeInstallUpdateError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			logger.Error("error while extract release: %s", zap.Error(err))
			return
		}

		go service.UpdateStatusWithMessage(service.InstallBegin, "正式开始安装")

		err = service.InstallerService.Install(*release, sysRoot)
		if err != nil {
			go service.PublishEventWrapper(context.Background(), common.EventTypeInstallUpdateError, map[string]string{
				common.PropertyTypeMessage.Name: err.Error(),
			})

			logger.Error("error while install system: %s", zap.Error(err))
			return
		}

	}()

	message := "release being installed asynchronously"
	return ctx.JSON(http.StatusOK, &codegen.ResponseOK{
		Message: &message,
	})
}
