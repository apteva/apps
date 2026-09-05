package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sync"
	"time"
)

type messageCacheEntry struct {
	expires  time.Time
	response messagingMessageGetResponse
}

var messageCache = struct {
	sync.Mutex
	entries map[string]messageCacheEntry
}{entries: map[string]messageCacheEntry{}}
var messageFetchLocks [32]sync.Mutex

// A short bounded cache coalesces simultaneous Inbox/CRM refreshes. Its key
// includes the CRM database and bound Messaging install, preventing rebind leaks.
func cachedMessagingMessage(ctx *sdk.AppCtx, pid string, id int64) (messagingMessageGetResponse, error) {
	key := fmt.Sprintf("%p/%s/%d/%d", ctx.AppDB(), pid, messagingInstallID(ctx), id)
	lock := &messageFetchLocks[uint64(id)%uint64(len(messageFetchLocks))]
	lock.Lock()
	defer lock.Unlock()
	messageCache.Lock()
	entry, ok := messageCache.entries[key]
	messageCache.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.response, nil
	}
	var out messagingMessageGetResponse
	err := callMessagingTool(ctx, "message_get", map[string]any{"_project_id": pid, "id": id}, &out)
	if err == nil && out.Found {
		messageCache.Lock()
		if len(messageCache.entries) >= 2000 {
			now := time.Now()
			for key, e := range messageCache.entries {
				if now.After(e.expires) {
					delete(messageCache.entries, key)
				}
			}
			if len(messageCache.entries) >= 2000 {
				for key := range messageCache.entries {
					delete(messageCache.entries, key)
					break
				}
			}
		}
		messageCache.entries[key] = messageCacheEntry{expires: time.Now().Add(3 * time.Second), response: out}
		messageCache.Unlock()
	}
	return out, err
}
