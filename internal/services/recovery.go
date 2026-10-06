package services

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"time"

	"gostalgia/internal/ipc"
	"gostalgia/internal/recovery"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
)

// RecoveryService exposes backup export, inspection, preview, and restore over IPC.
// It owns the "backup/*" method namespace.
type RecoveryService struct {
	ctx *service.Context
}

// NewRecovery creates a new RecoveryService.
func NewRecovery() *RecoveryService {
	return &RecoveryService{}
}

func (s *RecoveryService) Name() string { return "recovery" }

func (s *RecoveryService) Depends() []string {
	return []string{"fs"}
}

func (s *RecoveryService) Init(ctx *service.Context) error {
	s.ctx = ctx
	return nil
}

func (s *RecoveryService) Start(ctx context.Context) error {
	routes := map[string]ipc.Handler{
		"backup/export":  s.exportBackup,
		"backup/inspect": s.inspectBackup,
		"backup/preview": s.previewBackup,
		"backup/restore": s.restoreBackup,
	}

	for route, handler := range routes {
		if err := s.ctx.Router.Handle(route, handler); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecoveryService) Stop(ctx context.Context) error {
	s.ctx.Router.UnhandlePrefix("backup/")
	return nil
}

func (s *RecoveryService) requireReadCap(ctx context.Context) error {
	if ipc.RequireCap(ctx, security.CapBackupRead) == nil {
		return nil
	}
	if ipc.RequireCap(ctx, security.CapAdmin) == nil {
		return nil
	}
	if ipc.CallerPrincipal(ctx).IsOperator() {
		return nil
	}
	return ipc.RequireCap(ctx, security.CapBackupRead)
}

func (s *RecoveryService) requireWriteCap(ctx context.Context) error {
	if ipc.RequireCap(ctx, security.CapBackupWrite) == nil {
		return nil
	}
	if ipc.RequireCap(ctx, security.CapAdmin) == nil {
		return nil
	}
	if ipc.CallerPrincipal(ctx).IsOperator() {
		return nil
	}
	return ipc.RequireCap(ctx, security.CapBackupWrite)
}

// Type aliases for IPC parameter payloads
type BackupExportParams = recovery.ExportParams
type BackupPathParams = recovery.PathParams
type BackupRestoreParams = recovery.RestoreParams

func (s *RecoveryService) exportBackup(ctx context.Context, req ipc.Request) (any, error) {
	if err := s.requireWriteCap(ctx); err != nil {
		return nil, err
	}

	var p BackupExportParams
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}

	destPath := p.Path
	if destPath == "" {
		targetProfile := p.ProfileID
		if targetProfile == "" && s.ctx.Profiles != nil {
			targetProfile = s.ctx.Profiles.ActiveID()
		}
		if targetProfile == "" {
			targetProfile = "guest"
		}
		destPath = fmt.Sprintf("/users/%s/downloads/backup-%d%s", targetProfile, time.Now().Unix(), recovery.FileExtension)
	}

	includeSystem := true
	if p.IncludeSystem != nil {
		includeSystem = *p.IncludeSystem
	}

	excludeSecrets := p.ExcludeSecrets
	if s.ctx.Token != "" {
		excludeSecrets = append(excludeSecrets, s.ctx.Token)
	}

	opts := recovery.ExportOptions{
		ProfileID:      p.ProfileID,
		IncludeSystem:  includeSystem,
		ExcludeSecrets: excludeSecrets,
		SourceVersion:  s.ctx.Version,
		Description:    p.Description,
	}

	res, err := recovery.ExportToVFS(ctx, s.ctx.VFS, destPath, opts)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (s *RecoveryService) inspectBackup(ctx context.Context, req ipc.Request) (any, error) {
	if err := s.requireReadCap(ctx); err != nil {
		return nil, err
	}

	var p BackupPathParams
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("recovery: path parameter is required")
	}

	cleanPath := path.Clean(p.Path)
	data, err := s.ctx.VFS.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("recovery: read archive %q: %w", cleanPath, err)
	}

	manifest, err := recovery.Inspect(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (s *RecoveryService) previewBackup(ctx context.Context, req ipc.Request) (any, error) {
	if err := s.requireReadCap(ctx); err != nil {
		return nil, err
	}

	var p BackupPathParams
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("recovery: path parameter is required")
	}

	cleanPath := path.Clean(p.Path)
	data, err := s.ctx.VFS.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("recovery: read archive %q: %w", cleanPath, err)
	}

	report, err := recovery.Preview(bytes.NewReader(data), s.ctx.VFS)
	if err != nil {
		return nil, err
	}
	return report, nil
}

func (s *RecoveryService) restoreBackup(ctx context.Context, req ipc.Request) (any, error) {
	if err := s.requireWriteCap(ctx); err != nil {
		return nil, err
	}

	var p BackupRestoreParams
	if err := ipc.DecodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("recovery: path parameter is required")
	}

	strategy := p.Strategy
	if strategy == "" {
		strategy = recovery.ConflictAbort
	}
	switch strategy {
	case recovery.ConflictAbort, recovery.ConflictOverwrite, recovery.ConflictSkip:
	default:
		return nil, fmt.Errorf("recovery: invalid conflict strategy %q (allowed: abort, overwrite, skip)", strategy)
	}

	cleanPath := path.Clean(p.Path)
	data, err := s.ctx.VFS.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("recovery: read archive %q: %w", cleanPath, err)
	}

	opts := recovery.RestoreOptions{
		Strategy:      strategy,
		ProfileFilter: p.ProfileFilter,
	}

	rep, err := recovery.Restore(ctx, bytes.NewReader(data), s.ctx.VFS, opts)
	if err != nil {
		return nil, err
	}

	// If profiles registry was restored, reload profiles manager if present
	if s.ctx.Profiles != nil {
		_ = s.ctx.Profiles.Reload()
	}

	return rep, nil
}
