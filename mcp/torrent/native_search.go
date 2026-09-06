package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
)

// Separate clients keep searches from mutating active download priorities.
var nativeSearchSlots = make(chan struct{}, 2)

func nativeMagnet(query string) (string, bool, error) {
	query = strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToLower(query), "magnet:") {
		if infohashFromMagnet(query) == "" {
			return "", true, errors.New("native lookup requires a v1 or hybrid magnet with a valid btih hash")
		}
		return query, true, nil
	}
	if hash := normalizeInfohash(query); hash != "" {
		return "magnet:?xt=urn:btih:" + hash, true, nil
	}
	return "", false, nil
}

func (e *Engine) resolveMetadata(ctx context.Context, magnet string) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	select {
	case nativeSearchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-nativeSearchSlots }()
	spec, err := torrent.TorrentSpecFromMagnetUri(magnet)
	if err != nil {
		return nil, fmt.Errorf("invalid magnet: %w", err)
	}
	hasTracker := false
	for _, tier := range spec.Trackers {
		hasTracker = hasTracker || len(tier) > 0
	}
	if !e.cfg.DHTEnabled && len(spec.PeerAddrs) == 0 && !hasTracker {
		return nil, errors.New("DHT is disabled; enable it or supply a magnet with trackers or peer addresses")
	}
	dir, err := os.MkdirTemp("", "apteva-torrent-metadata-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cfg := e.cfg
	cfg.WorkingDir, cfg.ListenPort, cfg.MaxConcurrent = dir, 0, 1
	lookup, err := NewEngine(cfg, nil)
	if err != nil {
		return nil, err
	}
	defer lookup.Close()
	// Resolve BEP-9 metadata without torrent payload or HTTP sources.
	spec.DisallowDataDownload, spec.DisallowDataUpload = true, true
	spec.Webseeds, spec.Sources = nil, nil
	t, _, err := lookup.cli.AddTorrentSpec(spec)
	if err != nil {
		return nil, err
	}
	select {
	case <-t.GotInfo():
		return []SearchResult{{Name: t.Name(), Infohash: t.InfoHash().HexString(), Magnet: magnet,
			SizeBytes: t.Length(), Indexer: "BitTorrent metadata", AvailabilityUnknown: true}}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("metadata lookup: %w; no peer supplied metadata in time (the swarm may be offline or UDP blocked)", ctx.Err())
	}
}
