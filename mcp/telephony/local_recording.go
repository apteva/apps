package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const localRecordingProvider = "telephony"

type localRecordingFrame struct {
	direction int
	pcm       []int16
	at        time.Time
}
type localCallRecorder struct {
	app        *App
	appCtx     *sdk.AppCtx
	row        *recordingRow
	file       *os.File
	queue      chan localRecordingFrame
	stop, done chan struct{}
	once       sync.Once
	producer   sync.RWMutex
	mono       bool
	started    time.Time
	paused     atomic.Bool
	dropped    atomic.Int64
}

func (a *App) newLocalRecording(call *callRow, voicemail bool) (*localCallRecorder, error) {
	if !voicemail && call.RecordingMode != recordingModeAlways {
		return nil, nil
	}
	var seq int
	var name, file string
	_ = a.db().db.QueryRow(`SELECT seq,name,file FROM pragma_database_list WHERE name='main'`).Scan(&seq, &name, &file)
	root := filepath.Join(filepath.Dir(file), "recordings")
	if file == "" {
		var err error
		root, err = os.MkdirTemp("", "telephony-recordings-")
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(root, "call-*.wav")
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	if _, err = f.Write(make([]byte, 44)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	track := "both"
	if voicemail {
		track = "voicemail"
	}
	row, err := a.db().upsertProviderRecording(call, localRecordingProvider, "local-"+newCallID(), "in-progress", "wav", 0, 2, track)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	// A non-completed row is never imported or advertised as playable.
	persistedPath := f.Name()
	if a.runtime != nil {
		persistedPath = filepath.Join("recordings", filepath.Base(f.Name()))
	}
	if _, err = a.db().db.Exec(`INSERT INTO local_recording_files(recording_id,path) VALUES(?,?)`, row.ID, persistedPath); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	r := &localCallRecorder{app: a, appCtx: globalCtx, row: row, file: f, queue: make(chan localRecordingFrame, 128), stop: make(chan struct{}), done: make(chan struct{}), started: time.Now(), mono: call.RecordingChannels == "mono" || voicemail}
	a.recordingWriters.Add(1)
	go r.run()
	return r, nil
}
func (r *localCallRecorder) Record(direction int, pcm []int16) {
	if r == nil || r.paused.Load() || direction < 0 || direction > 1 || len(pcm) == 0 {
		return
	}
	r.producer.RLock()
	defer r.producer.RUnlock()
	if len(pcm) > 24000 {
		r.dropped.Add(int64(len(pcm) * 1000 / 24000))
		return
	}
	select {
	case <-r.stop:
		return
	default:
	}
	frame := localRecordingFrame{direction, append([]int16(nil), pcm...), time.Now()}
	select {
	case r.queue <- frame:
	default:
		r.dropped.Add(int64(len(pcm) * 1000 / 24000))
	}
}
func (r *localCallRecorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { r.producer.Lock(); close(r.stop); r.producer.Unlock() })
	<-r.done
}

// Each direction is laid out by media time: frames continue back-to-back from
// the previous one, and wall time (Record arrival) only re-anchors a track after
// a real gap. Frames arriving later than their contiguous slot are held until a
// later frame shows whether they were a delayed burst (still contiguous) or the
// start of audio after a silence (placed where they arrived on time).
const (
	localRecordingJitterSamples  = 24000 * 60 / 1000 // arrival slack still treated as contiguous
	localRecordingResyncWindow   = time.Second       // wall time a late run waits for a catch-up frame
	localRecordingPendingSamples = 2 * 24000         // bound on held late audio per direction
)

type localRecordingTrack struct {
	started        bool
	next           int64
	pending        []localRecordingFrame
	pendingSamples int64
}

func (r *localCallRecorder) run() {
	defer r.app.recordingWriters.Add(-1)
	defer close(r.done)
	defer r.file.Close()
	var tracks [2]localRecordingTrack
	var length int64
	var cause error
	place := func(direction int, sample int64, pcm []int16) {
		if cause != nil {
			return
		}
		sample = max(sample, 0)
		end := sample + int64(len(pcm))
		if end*4 > 3<<30 {
			cause = errors.New("local recording exceeds size limit")
			return
		}
		// Disk work is isolated to this worker. Preserve the opposite channel,
		// and leave sparse silence for real gaps instead of compressing the timeline.
		raw := make([]byte, len(pcm)*4)
		_, err := r.file.ReadAt(raw, 44+sample*4)
		if err != nil && !errors.Is(err, io.EOF) {
			cause = err
			return
		}
		for i, p := range pcm {
			binary.LittleEndian.PutUint16(raw[i*4+direction*2:], uint16(p))
		}
		if _, err = r.file.WriteAt(raw, 44+sample*4); err != nil {
			cause = err
			return
		}
		tracks[direction].next = end
		length = max(length, end*4)
	}
	wallSample := func(at time.Time) int64 {
		return max(at.Sub(r.started).Nanoseconds()*24000/int64(time.Second), 0)
	}
	resolve := func(direction int) {
		t := &tracks[direction]
		frames := t.pending
		t.pending, t.pendingSamples = nil, 0
		// starts[k]: where the held run would begin if frame k arrived on time.
		// Delivery delay only makes a frame later, so the minimum over frame k and
		// everything after it is the earliest position consistent with that frame.
		starts := make([]int64, len(frames))
		var offset int64
		for k, f := range frames {
			starts[k] = wallSample(f.at) - offset
			offset += int64(len(f.pcm))
		}
		for k := len(starts) - 2; k >= 0; k-- {
			starts[k] = min(starts[k], starts[k+1])
		}
		anchor := t.next
		offset = 0
		for k, f := range frames {
			if starts[k]-anchor > localRecordingJitterSamples {
				anchor = starts[k] // no later frame catches up: a real gap precedes frame k
			}
			place(direction, anchor+offset, f.pcm)
			offset += int64(len(f.pcm))
		}
	}
	write := func(frame localRecordingFrame) {
		if cause != nil {
			return
		}
		t := &tracks[frame.direction]
		if !t.started {
			t.started = true
			place(frame.direction, wallSample(frame.at), frame.pcm)
			return
		}
		t.pending = append(t.pending, frame)
		if wallSample(frame.at)-(t.next+t.pendingSamples) <= localRecordingJitterSamples {
			resolve(frame.direction)
			return
		}
		t.pendingSamples += int64(len(frame.pcm))
		if frame.at.Sub(t.pending[0].at) >= localRecordingResyncWindow || t.pendingSamples >= localRecordingPendingSamples {
			resolve(frame.direction)
		}
	}
	for {
		select {
		case frame := <-r.queue:
			write(frame)
		case <-r.stop:
			goto drain
		}
	}
drain:
	for {
		select {
		case frame := <-r.queue:
			write(frame)
		default:
			goto finish
		}
	}
finish:
	for direction := range tracks {
		resolve(direction)
	}
	if cause == nil {
		_, cause = r.file.Seek(0, io.SeekStart)
		if cause == nil {
			cause = writeDualLocalWAVHeader(r.file, 24000, length)
		}
	}
	if cause == nil {
		cause = r.file.Sync()
	}
	if length == 0 && cause == nil {
		cause = errors.New("no recorded audio")
	}
	channels := 2
	if cause == nil && r.mono {
		var monoPath string
		monoPath, cause = buildRecordingVariant(r.file.Name(), recordingVariantMix)
		if cause == nil {
			_ = r.file.Close()
			cause = os.Rename(monoPath, r.file.Name())
			if cause != nil {
				_ = os.Remove(monoPath)
			} else {
				length /= 2
				channels = 1
			}
		}
	}
	status, storage, message := "completed", "pending", ""
	if cause != nil {
		status, storage, message = "failed", "failed", "local recording write failed"
		_ = r.file.Close()
		_ = os.Remove(r.file.Name())
	}
	if dropped := r.dropped.Load(); dropped > 0 {
		omitted := fmt.Sprintf("recording incomplete: %d ms omitted by bounded recorder", dropped)
		if message != "" {
			// The write failure stays the primary reason; omission is secondary detail.
			message += "; " + omitted
		} else {
			message = omitted
		}
	}
	_, _ = r.app.db().db.Exec(`UPDATE recordings SET provider_status=?,storage_status=?,duration_ms=?,size_bytes=?,source_size_bytes=?,channels=?,last_error=?,completed_at=?,next_attempt_at=? WHERE id=? AND deleted_at=''`, status, storage, length*1000/int64(24000*2*channels), length+44, length+44, channels, message, ringTime(time.Now()), ringTime(time.Now()), r.row.ID)
	_, _ = r.app.db().db.Exec(`UPDATE local_recording_files SET dropped_ms=? WHERE recording_id=?`, r.dropped.Load(), r.row.ID)
	if r.appCtx != nil {
		r.app.saveCallNotification(r.row.ProjectID, r.row.CallID, "recording.ready", map[string]any{"id": r.row.ID, "call_id": r.row.CallID, "provider": localRecordingProvider, "provider_status": status, "dropped_ms": r.dropped.Load()})
	}
}
func writeDualLocalWAVHeader(w io.Writer, rate int, size int64) error {
	if size < 0 || size > int64(^uint32(0))-36 {
		return errors.New("recording exceeds WAV size")
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(size+36))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 2)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*4))
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(size))
	_, err := w.Write(h)
	return err
}
func (a *App) downloadLocalRecording(ctx context.Context, row *recordingRow) (string, int64, error) {
	var path string
	if err := a.db().db.QueryRow(`SELECT path FROM local_recording_files WHERE recording_id=?`, row.ID).Scan(&path); err != nil {
		return "", 0, err
	}
	path, err := a.resolveRecordingPath(path)
	if err != nil {
		return "", 0, err
	}
	source, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer source.Close()
	limit := recordingDownloadLimit(ctx)
	info, err := source.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() > limit {
		return "", 0, &recordingSizeLimitError{limit}
	}
	out, err := newRecordingTemporaryFile("telephony-local-playback-*.wav")
	if err != nil {
		return "", 0, err
	}
	good := false
	defer func() {
		out.Close()
		if !good {
			os.Remove(out.Name())
		}
	}()
	size, err := io.Copy(out, io.LimitReader(&localRecordingContextReader{ctx, source}, limit+1))
	if err != nil {
		return "", 0, err
	}
	if size > limit {
		return "", 0, &recordingSizeLimitError{limit}
	}
	if err = out.Close(); err != nil {
		return "", 0, err
	}
	good = true
	return out.Name(), size, nil
}

type localRecordingContextReader struct {
	ctx context.Context
	io.Reader
}

func (r *localRecordingContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
func (a *App) deleteLocalRecording(row *recordingRow) error {
	var path string
	if err := a.db().db.QueryRow(`SELECT path FROM local_recording_files WHERE recording_id=?`, row.ID).Scan(&path); err != nil {
		return err
	}
	path, err := a.resolveRecordingPath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err = a.db().db.Exec(`UPDATE recordings SET provider_deleted_at=? WHERE id=?`, ringTime(time.Now()), row.ID)
	return err
}
func (a *App) downloadLocalRecordingVariant(ctx context.Context, row *recordingRow, variant string) (string, error) {
	path, _, err := a.downloadLocalRecording(ctx, row)
	if err != nil {
		return "", err
	}
	out, err := buildRecordingVariant(path, variant, ctx)
	if err != nil || out != path {
		os.Remove(path)
	}
	return out, err
}
