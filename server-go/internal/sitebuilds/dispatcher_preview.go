package sitebuilds

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// deployPreview publishes the output to the build's preview, retrying while another deploy of the project runs.
func (e *execution) deployPreview(ctx context.Context, spool *os.File) outcome {
	service, build, options := e.dispatcher.service, e.build, e.dispatcher.options
	if service.options.Previews == nil {
		return failedWith(FailPlatform, "previews were turned off while this build ran; nothing was deployed")
	}
	request := sites.PreviewDeployRequest{ProjectID: build.ProjectID, SiteID: build.SiteID, PreviewID: build.PreviewID,
		CommitSHA: build.CommitSHA, APIAccess: build.Preview.APIAccess && !build.Preview.FromFork}
	var lastErr error
	for try := 0; try < options.DeployTries; try++ {
		if _, err := spool.Seek(0, 0); err != nil {
			return failedWith(FailPlatform, userMessage(err))
		}
		request.Body = spool
		preview, err := service.options.Previews.DeployPreview(ctx, request)
		if err == nil {
			return outcome{status: StatusSucceeded, previewURL: preview.URL()}
		}
		if !busy(err) {
			return failedWith(FailDeploy, previewMessage(err))
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return failedWith(FailDeploy, userMessage(ctx.Err()))
		case <-time.After(options.DeployRetry):
		}
	}
	return failedWith(FailDeploy, userMessage(lastErr))
}

func previewMessage(err error) string {
	if errors.Is(err, sites.ErrPreviewNotFound) {
		return "the preview was removed while this build ran; nothing was deployed"
	}
	return userMessage(err)
}

// reportPreview records a failed preview build on the preview and posts the outcome on the commit.
func (e *execution) reportPreview(ctx context.Context, result outcome) {
	service, build := e.dispatcher.service, e.build
	if build.Preview == nil || service.options.Previews == nil {
		return
	}
	if result.status == StatusSucceeded {
		service.postStatus(ctx, build, gitprovider.StatusSuccess, result.previewURL, "Preview ready")
		return
	}
	if err := service.options.Previews.RecordPreviewFailure(ctx, build.ProjectID, build.SiteID, build.PreviewID, result.reason); err != nil {
		log.Printf("site build %s: record the failed preview: %v", build.ID, err)
	}
	service.postStatus(ctx, build, gitprovider.StatusFailure, "", result.reason)
}
