package handlers

import (
	"context"

	"github.com/Bengo-Hub/httpware/objectstore"
	"go.uber.org/zap"
)

// releaseFile deletes a stored file a row stopped using (replaced or removed), so storage holds
// only files in use. Only this service's own keys are deleted: an old media volume file or an
// external URL may be shared and is left alone, as is a file inUse still reports in use. Call it
// after the row is saved, so a failed save never points at a deleted file. Best effort.
func releaseFile(ctx context.Context, media *objectstore.Store, log *zap.Logger, old, current string, inUse func(key string) bool) {
	if old == "" || !media.Owned(old) {
		return
	}
	key := media.Normalize(old)
	if key == media.Normalize(current) || (inUse != nil && inUse(key)) {
		return
	}
	if err := media.Delete(ctx, key); err != nil && log != nil {
		log.Warn("replaced file not deleted from storage", zap.String("key", key), zap.Error(err))
	}
}
