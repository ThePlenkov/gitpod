// Copyright (c) 2020 Gitpod GmbH. All rights reserved.
// Licensed under the GNU Affero General Public License (AGPL).
// See License-AGPL.txt in the project root for license information.

package integration

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/xerrors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gitpod-io/gitpod/common-go/namegen"
	csapi "github.com/gitpod-io/gitpod/content-service/api"
	protocol "github.com/gitpod-io/gitpod/gitpod-protocol"
	imgbldr "github.com/gitpod-io/gitpod/image-builder/api"
	wsmanapi "github.com/gitpod-io/gitpod/ws-manager/api"
)

const (
	gitpodBuiltinUserID = "builtin-user-workspace-probe-0000000"
	perCallTimeout      = 5 * time.Minute
)

type launchWorkspaceDirectlyOptions struct {
	BaseImage   string
	IdeImage    string
	Mods        []func(*wsmanapi.StartWorkspaceRequest) error
	WaitForOpts []WaitForWorkspaceOpt
}

// LaunchWorkspaceDirectlyOpt configures the behaviour of LaunchWorkspaceDirectly
type LaunchWorkspaceDirectlyOpt func(*launchWorkspaceDirectlyOptions) error

// WithoutWorkspaceImage prevents the image-builder based base image resolution and sets
// the workspace image to an empty string.
// Usually callers would then use WithRequestModifier to set the workspace image themselves.
func WithoutWorkspaceImage() LaunchWorkspaceDirectlyOpt {
	return func(lwdo *launchWorkspaceDirectlyOptions) error {
		lwdo.BaseImage = ""
		return nil
	}
}

// WithBaseImage configures the base image used to start the workspace. The base image
// will be resolved to a workspace image using the image builder. If the corresponding
// workspace image isn't built yet, it will NOT be built.
func WithBaseImage(baseImage string) LaunchWorkspaceDirectlyOpt {
	return func(lwdo *launchWorkspaceDirectlyOptions) error {
		lwdo.BaseImage = baseImage
		return nil
	}
}

// WithIDEImage configures the IDE image used to start the workspace. Using this option
// as compared to setting the image using a modifier prevents the image ref computation
// based on the server's configuration.
func WithIDEImage(ideImage string) LaunchWorkspaceDirectlyOpt {
	return func(lwdo *launchWorkspaceDirectlyOptions) error {
		lwdo.IdeImage = ideImage
		return nil
	}
}

// WithRequestModifier modifies the start workspace request before it's sent.
func WithRequestModifier(mod func(*wsmanapi.StartWorkspaceRequest) error) LaunchWorkspaceDirectlyOpt {
	return func(lwdo *launchWorkspaceDirectlyOptions) error {
		lwdo.Mods = append(lwdo.Mods, mod)
		return nil
	}
}

// WithWaitWorkspaceForOpts adds options to the WaitForWorkspace call that happens as part of LaunchWorkspaceDirectly
func WithWaitWorkspaceForOpts(opt ...WaitForWorkspaceOpt) LaunchWorkspaceDirectlyOpt {
	return func(lwdo *launchWorkspaceDirectlyOptions) error {
		lwdo.WaitForOpts = opt
		return nil
	}
}

// LaunchWorkspaceDirectlyResult is returned by LaunchWorkspaceDirectly
type LaunchWorkspaceDirectlyResult struct {
	Req        *wsmanapi.StartWorkspaceRequest
	IdeURL     string
	LastStatus *wsmanapi.WorkspaceStatus
}

// LaunchWorkspaceDirectly starts a workspace pod by talking directly to ws-manager.
// Whenever possible prefer this function over LaunchWorkspaceFromContextURL, because
// it has fewer prerequisites.
func LaunchWorkspaceDirectly(ctx context.Context, api *ComponentAPI, opts ...LaunchWorkspaceDirectlyOpt) (*LaunchWorkspaceDirectlyResult, func(waitForStop bool) error, error) {
	options := launchWorkspaceDirectlyOptions{
		BaseImage: "docker.io/gitpod/workspace-full:latest",
	}
	for _, o := range opts {
		err := o(&options)
		if err != nil {
			return nil, nil, err
		}
	}

	instanceID, err := uuid.NewRandom()
	if err != nil {
		return nil, nil, err

	}
	workspaceID, err := namegen.GenerateWorkspaceID()
	if err != nil {
		return nil, nil, err
	}

	var workspaceImage string
	if options.BaseImage != "" {
		for {
			workspaceImage, err = resolveOrBuildImage(ctx, api, options.BaseImage)
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
				time.Sleep(5 * time.Second)
				continue
			} else if err != nil {
				return nil, nil, xerrors.Errorf("cannot resolve base image: %v", err)
			}
			break
		}
	}
	if workspaceImage == "" {
		return nil, nil, xerrors.Errorf("cannot start workspaces without a workspace image (required by registry-facade resolver)")
	}

	ideImage := options.IdeImage
	if ideImage == "" {
		cfg, err := GetServerIDEConfig(api.namespace, api.client)
		if err != nil {
			return nil, nil, xerrors.Errorf("cannot find server IDE config: %q", err)
		}
		ideImage = cfg.IDEOptions.Options.Code.Image
		if ideImage == "" {
			return nil, nil, xerrors.Errorf("cannot start workspaces without an IDE image (required by registry-facade resolver)")
		}
	}

	req := &wsmanapi.StartWorkspaceRequest{
		Id:            instanceID.String(),
		ServicePrefix: instanceID.String(),
		Metadata: &wsmanapi.WorkspaceMetadata{
			Owner:  gitpodBuiltinUserID,
			MetaId: workspaceID,
		},
		Type: wsmanapi.WorkspaceType_REGULAR,
		Spec: &wsmanapi.StartWorkspaceSpec{
			WorkspaceImage:     workspaceImage,
			DeprecatedIdeImage: ideImage,
			IdeImage: &wsmanapi.IDEImage{
				WebRef: ideImage,
			},
			WorkspaceLocation: "/",
			Timeout:           "30m",
			Initializer: &csapi.WorkspaceInitializer{
				Spec: &csapi.WorkspaceInitializer_Empty{
					Empty: &csapi.EmptyInitializer{},
				},
			},
			Git: &wsmanapi.GitSpec{
				Username: "integration-test",
				Email:    "integration-test@gitpod.io",
			},
			Admission: wsmanapi.AdmissionLevel_ADMIT_OWNER_ONLY,
		},
	}
	for _, m := range options.Mods {
		err := m(req)
		if err != nil {
			return nil, nil, err
		}
	}

	sctx, scancel := context.WithTimeout(ctx, perCallTimeout)
	defer scancel()

	wsm, err := api.WorkspaceManager()
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot start workspace manager: %q", err)
	}

	sresp, err := wsm.StartWorkspace(sctx, req)
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot start workspace: %q", err)
	}

	stopWs := func(waitForStop bool) error {
		tctx, tcancel := context.WithTimeout(context.Background(), perCallTimeout)
		defer tcancel()

		for {
			err = DeleteWorkspace(tctx, api, req.Id)
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
				time.Sleep(5 * time.Second)
				continue
			} else if err != nil {
				return err
			}
			break
		}
		for {
			_, err = WaitForWorkspaceStop(tctx, api, req.Id)
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
				time.Sleep(5 * time.Second)
				continue
			} else if err != nil {
				return err
			}
			break
		}
		return err
	}
	defer func() {
		if err != nil {
			stopWs(false)
		}
	}()

	lastStatus, err := WaitForWorkspaceStart(ctx, instanceID.String(), api, options.WaitForOpts...)
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot wait for workspace start: %q", err)
	}

	return &LaunchWorkspaceDirectlyResult{
		Req:        req,
		IdeURL:     sresp.Url,
		LastStatus: lastStatus,
	}, stopWs, nil
}

// LaunchWorkspaceFromContextURL force-creates a new workspace using the Gitpod server API,
// and waits for the workspace to start. If any step along the way fails, this function will
// fail the test.
//
// When possible, prefer the less complex LaunchWorkspaceDirectly.
func LaunchWorkspaceFromContextURL(ctx context.Context, contextURL string, username string, api *ComponentAPI, serverOpts ...GitpodServerOpt) (*protocol.WorkspaceInfo, func(waitForStop bool), error) {
	var defaultServerOpts []GitpodServerOpt
	if username != "" {
		defaultServerOpts = []GitpodServerOpt{WithGitpodUser(username)}
	}

	server, err := api.GitpodServer(append(defaultServerOpts, serverOpts...)...)
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot start server: %q", err)
	}

	cctx, ccancel := context.WithTimeout(context.Background(), perCallTimeout)
	defer ccancel()
	resp, err := server.CreateWorkspace(cctx, &protocol.CreateWorkspaceOptions{
		ContextURL: contextURL,
		Mode:       "force-new",
	})
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot start workspace: %q", err)
	}

	nfo, err := server.GetWorkspace(ctx, resp.CreatedWorkspaceID)
	if err != nil {
		return nil, nil, xerrors.Errorf("cannot get workspace: %q", err)
	}
	if nfo.LatestInstance == nil {
		return nil, nil, xerrors.Errorf("CreateWorkspace did not start the workspace")
	}

	// GetWorkspace might receive an instance before we seen the first event
	// from ws-manager, in which case IdeURL is not set
	nfo.LatestInstance.IdeURL = resp.WorkspaceURL

	stopWs := func(waitForStop bool) {
		sctx, scancel := context.WithTimeout(ctx, perCallTimeout)
		_ = server.StopWorkspace(sctx, resp.CreatedWorkspaceID)
		scancel()

		if waitForStop {
			_, _ = WaitForWorkspaceStop(ctx, api, nfo.LatestInstance.ID)
		}
	}
	defer func() {
		if err != nil {
			stopWs(false)
		}
	}()

	_, err = WaitForWorkspaceStart(ctx, nfo.LatestInstance.ID, api)
	if err != nil {
		return nil, nil, xerrors.Errorf("failed to wait for the workspace to start up: %w", err)
	}

	return nfo, stopWs, nil
}

// WaitForWorkspaceOpt configures a WaitForWorkspace call
type WaitForWorkspaceOpt func(*waitForWorkspaceOpts)

type waitForWorkspaceOpts struct {
	CanFail bool
}

// WorkspaceCanFail doesn't fail the test if the workspace fails to start
func WorkspaceCanFail(o *waitForWorkspaceOpts) {
	o.CanFail = true
}

// WaitForWorkspace waits until a workspace is running. Fails the test if the workspace
// fails or does not become RUNNING before the context is canceled.
func WaitForWorkspaceStart(ctx context.Context, instanceID string, api *ComponentAPI, opts ...WaitForWorkspaceOpt) (lastStatus *wsmanapi.WorkspaceStatus, err error) {
	var cfg waitForWorkspaceOpts
	for _, o := range opts {
		o(&cfg)
	}

	wsman, err := api.WorkspaceManager()
	if err != nil {
		return nil, err
	}

	var sub wsmanapi.WorkspaceManager_SubscribeClient
	for i := 0; i < 5; i++ {
		sub, err = wsman.Subscribe(ctx, &wsmanapi.SubscribeRequest{})
		if status.Code(err) == codes.NotFound {
			time.Sleep(1 * time.Second)
			continue
		}
		if err != nil {
			return nil, xerrors.Errorf("cannot listen for workspace updates: %w", err)
		}
		defer func() {
			_ = sub.CloseSend()
		}()
		break
	}

	done := make(chan *wsmanapi.WorkspaceStatus)
	errStatus := make(chan error)

	go func() {
		var s *wsmanapi.WorkspaceStatus
		defer func() {
			done <- s
			close(done)
		}()
		for {
			resp, err := sub.Recv()
			if err != nil {
				if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
					time.Sleep(10 * time.Second)
					continue
				}
				errStatus <- xerrors.Errorf("workspace update error: %w", err)
				return
			}

			s = resp.GetStatus()
			if s == nil {
				continue
			}
			if s.Id != instanceID {
				continue
			}

			if cfg.CanFail {
				if s.Phase == wsmanapi.WorkspacePhase_STOPPING {
					return
				}
				if s.Phase == wsmanapi.WorkspacePhase_STOPPED {
					return
				}
			} else {
				if s.Conditions.Failed != "" {
					errStatus <- xerrors.Errorf("workspace instance %s failed: %s", instanceID, s.Conditions.Failed)
					return
				}
				if s.Phase == wsmanapi.WorkspacePhase_STOPPING {
					errStatus <- xerrors.Errorf("workspace instance %s is stopping", instanceID)
					return
				}
				if s.Phase == wsmanapi.WorkspacePhase_STOPPED {
					errStatus <- xerrors.Errorf("workspace instance %s has stopped", instanceID)
					return
				}
			}
			if s.Phase != wsmanapi.WorkspacePhase_RUNNING {
				// we're still starting
				time.Sleep(10 * time.Second)
				continue
			}

			// all is well, the workspace is running
			return
		}
	}()

	// maybe the workspace has started in the meantime and we've missed the update
	desc, _ := wsman.DescribeWorkspace(ctx, &wsmanapi.DescribeWorkspaceRequest{Id: instanceID})
	if desc != nil {
		switch desc.Status.Phase {
		case wsmanapi.WorkspacePhase_RUNNING:
			return
		case wsmanapi.WorkspacePhase_STOPPING:
			if !cfg.CanFail {
				return nil, xerrors.Errorf("workspace instance %s is stopping", instanceID)
			}
		case wsmanapi.WorkspacePhase_STOPPED:
			if !cfg.CanFail {
				return nil, xerrors.Errorf("workspace instance %s has stopped", instanceID)
			}
		}
	}

	select {
	case <-ctx.Done():
		return nil, xerrors.Errorf("cannot wait for workspace: %w", ctx.Err())
	case s := <-done:
		return s, nil
	case err := <-errStatus:
		return nil, err
	}
}

// WaitForWorkspaceStop waits until a workspace is stopped. Fails the test if the workspace
// fails or does not stop before the context is canceled.
func WaitForWorkspaceStop(ctx context.Context, api *ComponentAPI, instanceID string) (lastStatus *wsmanapi.WorkspaceStatus, err error) {
	wsman, err := api.WorkspaceManager()
	if err != nil {
		return nil, xerrors.Errorf("cannot listen for workspace updates: %q", err)
	}

	sub, err := wsman.Subscribe(context.Background(), &wsmanapi.SubscribeRequest{})
	if err != nil {
		return nil, xerrors.Errorf("cannot listen for workspace updates: %q", err)
	}
	defer func() {
		_ = sub.CloseSend()
	}()

	done := make(chan struct{})
	errCh := make(chan error)
	go func() {
		defer close(done)
		for {
			resp, err := sub.Recv()
			if err != nil {
				errCh <- xerrors.Errorf("workspace update error: %q", err)
				return
			}
			status := resp.GetStatus()
			if status == nil {
				continue
			}
			if status.Id != instanceID {
				continue
			}

			if status.Conditions.Failed != "" {
				// TODO(toru): we have to fix https://github.com/gitpod-io/gitpod/issues/12021
				if status.Conditions.Failed == "The container could not be located when the pod was deleted.  The container used to be Running" || status.Conditions.Failed == "The container could not be located when the pod was terminated" {
					lastStatus = status
					return
				}
				errCh <- xerrors.Errorf("workspace instance %s failed: %s", instanceID, status.Conditions.Failed)
				return
			}
			if status.Phase == wsmanapi.WorkspacePhase_STOPPED {
				lastStatus = status
				return
			}

			time.Sleep(10 * time.Second)
		}
	}()

	// maybe the workspace has stopped in the meantime and we've missed the update
	desc, _ := wsman.DescribeWorkspace(context.Background(), &wsmanapi.DescribeWorkspaceRequest{Id: instanceID})
	if desc != nil {
		switch desc.Status.Phase {
		case wsmanapi.WorkspacePhase_STOPPED:
			// ensure theia service is cleaned up
			lastStatus = desc.Status
		}
	}

	select {
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, xerrors.Errorf("cannot wait for workspace: %q", ctx.Err())
	case <-done:
	}

	return
}

// WaitForWorkspace waits until the condition function returns true. Fails the test if the condition does
// not become true before the context is canceled.
func WaitForWorkspace(ctx context.Context, api *ComponentAPI, instanceID string, condition func(status *wsmanapi.WorkspaceStatus) bool) (lastStatus *wsmanapi.WorkspaceStatus, err error) {
	wsman, err := api.WorkspaceManager()
	if err != nil {
		return
	}

	sub, err := wsman.Subscribe(ctx, &wsmanapi.SubscribeRequest{})
	if err != nil {
		return nil, xerrors.Errorf("cannot listen for workspace updates: %q", err)
	}

	done := make(chan *wsmanapi.WorkspaceStatus, 1)
	errCh := make(chan error)

	var once sync.Once
	go func() {
		var status *wsmanapi.WorkspaceStatus
		defer func() {
			once.Do(func() {
				done <- status
				close(done)
			})
			_ = sub.CloseSend()
		}()
		for {
			resp, err := sub.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				errCh <- xerrors.Errorf("workspace update error: %q", err)
				return
			}
			status = resp.GetStatus()
			if status == nil {
				continue
			}
			if status.Id != instanceID {
				continue
			}

			if condition(status) {
				return
			}
		}
	}()

	// maybe the workspace has started in the meantime and we've missed the update
	desc, err := wsman.DescribeWorkspace(ctx, &wsmanapi.DescribeWorkspaceRequest{Id: instanceID})
	if err != nil {
		return nil, xerrors.Errorf("cannot get workspace: %q", err)
	}
	if condition(desc.Status) {
		once.Do(func() { close(done) })
		return desc.Status, nil
	}

	select {
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, xerrors.Errorf("cannot wait for workspace: %q", ctx.Err())
	case s := <-done:
		return s, nil
	}
}

func resolveOrBuildImage(ctx context.Context, api *ComponentAPI, baseRef string) (absref string, err error) {
	cl, err := api.ImageBuilder()
	if err != nil {
		return
	}

	reslv, err := cl.ResolveWorkspaceImage(ctx, &imgbldr.ResolveWorkspaceImageRequest{
		Source: &imgbldr.BuildSource{
			From: &imgbldr.BuildSource_Ref{
				Ref: &imgbldr.BuildSourceReference{
					Ref: baseRef,
				},
			},
		},
		Auth: &imgbldr.BuildRegistryAuth{
			Mode: &imgbldr.BuildRegistryAuth_Total{
				Total: &imgbldr.BuildRegistryAuthTotal{
					AllowAll: true,
				},
			},
		},
	})
	if err != nil {
		return
	}

	if reslv.Status == imgbldr.BuildStatus_done_success {
		return reslv.Ref, nil
	}

	bld, err := cl.Build(ctx, &imgbldr.BuildRequest{
		TriggeredBy: "integration-test",
		Source: &imgbldr.BuildSource{
			From: &imgbldr.BuildSource_Ref{
				Ref: &imgbldr.BuildSourceReference{
					Ref: baseRef,
				},
			},
		},
		Auth: &imgbldr.BuildRegistryAuth{
			Mode: &imgbldr.BuildRegistryAuth_Total{
				Total: &imgbldr.BuildRegistryAuthTotal{
					AllowAll: true,
				},
			},
		},
	})
	if err != nil {
		return
	}

	for {
		resp, err := bld.Recv()
		if err != nil {
			return "", err
		}

		if resp.Status == imgbldr.BuildStatus_done_success {
			break
		} else if resp.Status == imgbldr.BuildStatus_done_failure {
			return "", xerrors.Errorf("cannot build workspace image: %s", resp.Message)
		}
	}

	return reslv.Ref, nil
}

// DeleteWorkspace cleans up a workspace started during an integration test
func DeleteWorkspace(ctx context.Context, api *ComponentAPI, instanceID string) error {
	wm, err := api.WorkspaceManager()
	if err != nil {
		return err
	}

	_, err = wm.StopWorkspace(ctx, &wsmanapi.StopWorkspaceRequest{
		Id: instanceID,
	})
	if err != nil {
		return err
	}

	if err == nil {
		return nil
	}

	s, ok := status.FromError(err)
	if ok && s.Code() == codes.NotFound {
		return nil
	}

	return err
}
