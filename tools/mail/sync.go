package mail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

type SyncReport struct {
	Account     string
	Folder      string
	NewMessages int
	UIDValidity uint32
}

// Sync runs one pass of the account: per folder, fetches envelopes since
// last_uid, handles UIDVALIDITY changes by full-resyncing.
func Sync(ctx context.Context, store *Store, accountID uuid.UUID, account MailAccount, folders []string) ([]SyncReport, error) {
	var reports []SyncReport
	for _, name := range folders {
		folderID, lastUV, lastUID, err := store.EnsureFolder(ctx, accountID, name)
		if err != nil {
			return reports, err
		}
		envs, currentUV, err := account.SyncFolder(ctx, name, lastUID)
		if err != nil {
			return reports, fmt.Errorf("sync %s/%s: %w", account.Nickname(), name, err)
		}
		if lastUV != 0 && currentUV != lastUV {
			// UIDVALIDITY changed: discard cached state and refetch all.
			if err := store.ResetFolderState(ctx, folderID, currentUV); err != nil {
				return reports, err
			}
			envs, currentUV, err = account.SyncFolder(ctx, name, 0)
			if err != nil {
				return reports, fmt.Errorf("resync %s/%s: %w", account.Nickname(), name, err)
			}
		}

		newCount := 0
		newLastUID := lastUID
		for _, env := range envs {
			if _, err := store.InsertEnvelope(ctx, accountID, folderID, env); err != nil {
				return reports, fmt.Errorf("insert env uid=%d: %w", env.UID, err)
			}
			newCount++
			if env.UID > newLastUID {
				newLastUID = env.UID
			}
		}
		if err := store.UpdateFolderState(ctx, folderID, currentUV, newLastUID); err != nil {
			return reports, err
		}
		reports = append(reports, SyncReport{
			Account: account.Nickname(), Folder: name, NewMessages: newCount, UIDValidity: currentUV,
		})
	}
	return reports, nil
}

// GCAttachments removes attachment subdirectories older than ttlDays.
// Used by `darek mail sync` (Plan 4 Task 6).
func GCAttachments(dir string, ttlDays int) error {
	if dir == "" || ttlDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -ttlDays)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(full)
		}
	}
	return nil
}
