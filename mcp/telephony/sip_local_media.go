package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mp3 "github.com/hajimehoshi/go-mp3"
	"io"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Synthesis is independent of the carrier. A bound speech integration can
// return PCM or WAV; prerecorded project Storage files need no synthesizer.
type localMediaSettings struct {
	AudioProfileCodecs   bool               `json:"-"`
	RTPPlayback          *sipPlaybackPolicy `json:"rtp_playback,omitempty"`
	Codecs               []string           `json:"codecs,omitempty"`
	SpeechTool           string             `json:"speech_tool,omitempty"`
	SpeechInput          map[string]any     `json:"speech_input,omitempty"`
	SampleRate           int                `json:"sample_rate,omitempty"`
	Prompts              map[string]int64   `json:"prompts,omitempty"`
	GatherTimeoutSeconds int                `json:"gather_timeout_seconds,omitempty"`
	VoicemailMaxSeconds  int                `json:"voicemail_max_seconds,omitempty"`
}

func validateLocalMediaSettings(c localMediaSettings) error {
	if c.RTPPlayback != nil {
		if err := c.RTPPlayback.validate(); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, codec := range c.Codecs {
		if seen[codec] {
			return errors.New("duplicate SIP codec")
		}
		seen[codec] = true
		if codec != "G722" && codec != "PCMU" && codec != "PCMA" {
			return errors.New("codecs must contain G722, PCMU or PCMA")
		}
	}
	if len(c.Codecs) > 3 {
		return errors.New("at most three SIP codecs")
	}
	if c.SampleRate != 0 && c.SampleRate != 8000 && c.SampleRate != 16000 && c.SampleRate != 24000 && c.SampleRate != 48000 {
		return errors.New("sample_rate must be 8000, 16000, 24000 or 48000")
	}
	if len(c.SpeechTool) > 120 || strings.ContainsAny(c.SpeechTool, "\r\n ") || len(c.Prompts) > 100 {
		return errors.New("invalid local speech configuration")
	}
	if c.GatherTimeoutSeconds < 0 || c.GatherTimeoutSeconds > 60 || c.VoicemailMaxSeconds < 0 || c.VoicemailMaxSeconds > 600 {
		return errors.New("gather timeout must be 1–60 seconds and voicemail maximum 1–600 seconds")
	}
	for text, id := range c.Prompts {
		if strings.TrimSpace(text) == "" || len(text) > 4000 || id <= 0 {
			return errors.New("prompts must map nonempty text to project Storage file IDs")
		}
	}
	return nil
}
func (a *App) localMediaSettings(project, route string) (localMediaSettings, error) {
	var raw string
	err := a.db().db.QueryRow(`SELECT config_json FROM local_media_settings WHERE project_id=? AND route_id IN ('',?) ORDER BY CASE WHEN route_id=? THEN 0 ELSE 1 END LIMIT 1`, project, route, route).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return localMediaSettings{}, nil
	}
	if err != nil {
		return localMediaSettings{}, err
	}
	var c localMediaSettings
	err = json.Unmarshal([]byte(raw), &c)
	return c, err
}
func (a *App) saveLocalMediaSettings(ctx *sdk.AppCtx, routeID string, c localMediaSettings) error {
	if err := validateLocalMediaSettings(c); err != nil {
		return err
	}
	if routeID != "" {
		if _, err := a.routeForCaller(ctx, routeID, 0); err != nil {
			return err
		}
	}
	if c.SpeechTool != "" && ctx.IntegrationFor("speech") == nil {
		return errors.New("bind a speech integration before configuring speech_tool")
	}
	for _, id := range c.Prompts {
		var file struct {
			Found bool `json:"found"`
			File  struct {
				ProjectID string `json:"project_id"`
			} `json:"file"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": id}, &file); err != nil {
			return err
		}
		if !file.Found || file.File.ProjectID != ctx.CurrentProject() {
			return errors.New("prompt asset is not in this project")
		}
	}
	raw, _ := json.Marshal(c)
	_, err := a.db().db.Exec(`INSERT INTO local_media_settings(project_id,route_id,config_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,route_id) DO UPDATE SET config_json=excluded.config_json,updated_at=excluded.updated_at`, ctx.CurrentProject(), routeID, string(raw), ringTime(time.Now()))
	a.inventoryReads.invalidate()
	return err
}

var localSpeechSlots = make(chan struct{}, 4)

func (a *App) renderLocalSpeech(ctx context.Context, appCtx *sdk.AppCtx, c localMediaSettings, text, voice string) ([]int16, error) {
	if text == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if len(text) > 4000 {
		return nil, errors.New("prompt exceeds 4000 characters")
	}
	select {
	case localSpeechSlots <- struct{}{}:
		defer func() { <-localSpeechSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if id := c.Prompts[text]; id > 0 {
		path, _, err := newRecordingStorageClient().download(ctx, appCtx.CurrentProject(), id, "wav")
		if err != nil {
			return nil, err
		}
		defer os.Remove(path)
		return readLocalPromptAudio(path)
	}
	bound := appCtx.IntegrationFor("speech")
	if bound == nil || c.SpeechTool == "" {
		return nil, errors.New("configure a speech integration or a prerecorded prompt for direct SIP")
	}
	input := make(map[string]any, len(c.SpeechInput)+2)
	for k, v := range c.SpeechInput {
		input[k] = v
	}
	input["text"] = text
	if voice != "" {
		input["voice"] = voice
	}
	// Require the context-aware SDK path: a timed-out prompt must not leave an
	// unbounded carrier/media worker waiting for a synthesis provider.
	result, err := sdk.ExecuteIntegrationToolContext(ctx, appCtx.PlatformAPI(), bound.ConnectionID, c.SpeechTool, input)
	if err != nil {
		return nil, err
	}
	if result == nil || !result.Success {
		return nil, errors.New("speech generation failed")
	}
	var envelope struct {
		Binary bool   `json:"_binary"`
		Base64 string `json:"base64"`
		MIME   string `json:"mimeType"`
	}
	if len(result.Data) > 8<<20 || json.Unmarshal(result.Data, &envelope) != nil || !envelope.Binary {
		return nil, errors.New("speech tool must return a bounded binary PCM/WAV envelope")
	}
	raw, err := base64.StdEncoding.DecodeString(envelope.Base64)
	if err != nil {
		return nil, errors.New("invalid speech audio")
	}
	if strings.Contains(strings.ToLower(envelope.MIME), "mpeg") || strings.Contains(strings.ToLower(envelope.MIME), "mp3") {
		return decodeLocalMP3(bytes.NewReader(raw))
	}
	if strings.Contains(strings.ToLower(envelope.MIME), "wav") {
		file, err := os.CreateTemp("", "telephony-prompt-*.wav")
		if err != nil {
			return nil, err
		}
		defer os.Remove(file.Name())
		_, err = file.Write(raw)
		file.Close()
		if err != nil {
			return nil, err
		}
		return readLocalPromptWAV(file.Name())
	}
	if c.SampleRate == 0 || len(raw)%2 != 0 || len(raw) == 0 {
		return nil, errors.New("raw speech requires sample_rate and little-endian mono PCM16")
	}
	if len(raw) > c.SampleRate*2*120 {
		return nil, errors.New("speech exceeds 120 seconds")
	}
	return newPCMResampler(c.SampleRate, 24000).Process(bytesToPCM16(raw)), nil
}
func readLocalPromptAudio(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [4]byte
	_, err = io.ReadFull(f, magic[:])
	if err != nil {
		return nil, err
	}
	if string(magic[:]) == "RIFF" {
		return readLocalPromptWAV(path)
	}
	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}
	return decodeLocalMP3(f)
}
func decodeLocalMP3(source io.Reader) ([]int16, error) {
	decoder, err := mp3.NewDecoder(source)
	if err != nil {
		return nil, fmt.Errorf("prompt must be PCM WAV or MP3: %w", err)
	}
	rate := decoder.SampleRate()
	limit := rate * 4 * 120
	raw, err := io.ReadAll(io.LimitReader(decoder, int64(limit+1)))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("prompt exceeds 120 seconds")
	}
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil, errors.New("invalid MP3 audio")
	}
	pcm := bytesToPCM16(raw)
	mono := make([]int16, len(pcm)/2)
	for i := range mono {
		mono[i] = int16((int32(pcm[i*2]) + int32(pcm[i*2+1])) / 2)
	}
	return newPCMResampler(rate, 24000).Process(mono), nil
}
func readLocalPromptWAV(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := inspectPCM16WAV(f)
	if err != nil {
		return nil, err
	}
	if info.SampleRate < 8000 || info.SampleRate > 48000 || info.DataSize > int64(info.SampleRate*info.Channels*2*120) {
		return nil, errors.New("prompt WAV exceeds supported rate/duration")
	}
	if _, err = f.Seek(info.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}
	raw := make([]byte, info.DataSize)
	if _, err = io.ReadFull(f, raw); err != nil {
		return nil, err
	}
	pcm := bytesToPCM16(raw)
	if info.Channels == 2 {
		mono := make([]int16, len(pcm)/2)
		for i := range mono {
			mono[i] = int16((int32(pcm[i*2]) + int32(pcm[i*2+1])) / 2)
		}
		pcm = mono
	}
	return newPCMResampler(info.SampleRate, 24000).Process(pcm), nil
}

type sipLocalRuntime struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	digits chan string
	plans  map[string]bool
	config localMediaSettings
}

func (s *sipSession) stopLocalMedia() error {
	s.local.mu.Lock()
	cancel, done := s.local.cancel, s.local.done
	s.local.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("local SIP media did not drain")
	}
}
func (a *App) executeSIPRoutingPlan(ctx *sdk.AppCtx, row *callRow, plan *inboundRoutingPlan) error {
	g := a.directSIPGateway()
	if g == nil {
		return errors.New("SIP gateway unavailable")
	}
	s := g.sessionByCall(row.ID)
	if s == nil || s.ended.Load() {
		return errAnswerCallEnded
	}
	s.local.mu.Lock()
	if s.local.plans == nil {
		s.local.plans = map[string]bool{}
	}
	if s.local.plans[plan.NodeID] {
		s.local.mu.Unlock()
		return nil
	}
	if s.local.done != nil {
		select {
		case <-s.local.done:
		default:
			s.local.mu.Unlock()
			return errAnswerPreparationInProgress
		}
	}
	seconds := 180
	if plan.TerminalType == "voicemail" {
		seconds += max(120, s.local.config.VoicemailMaxSeconds)
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET state_expires_at=? WHERE id=? AND status='pending'`, ringTime(time.Now().Add(time.Duration(seconds)*time.Second)), row.ID); err != nil {
		s.local.mu.Unlock()
		return err
	}
	s.local.plans[plan.NodeID] = true
	localCtx, cancel := context.WithTimeout(s.ctx, time.Duration(180+max(120, s.local.config.VoicemailMaxSeconds))*time.Second)
	done := make(chan struct{})
	s.local.cancel = cancel
	s.local.done = done
	s.local.digits = make(chan string, 16)
	s.local.mu.Unlock()
	if !g.runTask(func() {
		err := func() error { defer cancel(); defer close(done); return s.runLocalPlan(ctx, plan, localCtx, done) }()
		if err == nil {
			_ = a.completeRoutingEffect(row.ID + ":" + plan.NodeID)
		}
		if err != nil && s.ctx.Err() == nil && !errors.Is(err, errAnswerCallEnded) && !errors.Is(err, context.Canceled) {
			ctx.Logger().Warn("local SIP routing failed", "call", row.ID, "node", plan.NodeID, "err", err)
			var status string
			_ = a.db().db.QueryRow(`SELECT status FROM calls WHERE id=?`, row.ID).Scan(&status)
			if status == "pending" || status == "answering" {
				s.finish("local_error", errors.New("local SIP routing failed"))
			}
		}
	}) {
		cancel()
		close(done)
		return errors.New("SIP gateway stopping")
	}
	return nil
}
func (s *sipSession) runLocalPlan(appCtx *sdk.AppCtx, plan *inboundRoutingPlan, ctx context.Context, done <-chan struct{}) error {
	a := s.gateway.app
	var resumeNode, resumeDigit string
	var activateAI bool
	defer func() {
		if activateAI && s.ctx.Err() == nil {
			s.gateway.runTask(func() {
				<-done
				if s.ctx.Err() != nil {
					return
				}
				fresh, err := a.db().findCall(s.call.ID)
				if err == nil && routingMayControlCall(fresh) {
					greeting := plan.Greeting
					prefix := terminalAnnouncementText(plan)
					if prefix != "" {
						greeting = strings.TrimSpace(strings.TrimPrefix(greeting, prefix))
					}
					_, err = a.answerCall(appCtx, fresh, plan.Directive, plan.Voice, greeting, false)
				}
				if err != nil {
					appCtx.Logger().Warn("activate SIP AI", "call", s.call.ID, "err", err)
				}
			})
		}
		if resumeNode != "" && s.ctx.Err() == nil {
			s.gateway.runTask(func() {
				<-done
				if s.ctx.Err() != nil {
					return
				}
				if err := s.resumeLocalRouting(appCtx, resumeNode, resumeDigit); err != nil {
					appCtx.Logger().Warn("resume local SIP routing", "call", s.call.ID, "err", err)
				}
			})
		}
	}()
	row, err := a.db().findCall(s.call.ID)
	if err != nil {
		return err
	}
	if !routingMayControlCall(row) {
		return errAnswerCallEnded
	}
	prompt := terminalAnnouncementText(plan)
	if plan.Prompt != "" {
		if prompt != "" {
			prompt += "\n"
		}
		prompt += plan.Prompt
	}
	interactive := prompt != "" || plan.TerminalType == "dtmf_menu" || plan.TerminalType == "voicemail"
	if interactive {
		s.local.mu.Lock()
		digits, cfg := s.local.digits, s.local.config
		s.local.mu.Unlock()
		// Render before answering; on timeout/cancellation no late result can answer.
		var speech []int16
		parts := localRoutingSpeechParts(plan)
		for _, part := range parts {
			audio, e := a.renderLocalSpeech(ctx, appCtx, cfg, part, plan.Voice)
			if e != nil {
				return e
			}
			speech = append(speech, audio...)
			if len(speech) > 24000*120 {
				return errors.New("routing prompts exceed 120 seconds")
			}
		}
		var err error
		if ctx.Err() != nil || s.ended.Load() {
			return errAnswerCallEnded
		}
		if err = s.answer(); err != nil {
			return err
		}
		select {
		case <-s.initialACK:
		case <-ctx.Done():
			return ctx.Err()
		}
		if _, err = a.db().db.Exec(`UPDATE calls SET carrier_answered_at=CASE WHEN carrier_answered_at='' THEN ? ELSE carrier_answered_at END WHERE id=? AND status='pending'`, ringTime(time.Now()), row.ID); err != nil {
			return err
		}
		s.answerMu.Lock()
		media := s.media
		s.answerMu.Unlock()
		if media == nil {
			return errAnswerCallEnded
		}
		mediaCtx, stop := context.WithCancel(ctx)
		defer stop()
		var reads sync.WaitGroup
		reads.Add(1)
		go func() { defer reads.Done(); s.readLocalRTP(mediaCtx, media, digits, plan.TerminalType == "voicemail") }()
		defer func() { stop(); reads.Wait() }()
		playback := &sipPlaybackState{}
		pacer := newSIPRTPPacer(mediaCtx, media, playback, nil)
		defer func() { stop(); <-pacer.done }()
		if err = playSIPPCM(mediaCtx, media, pacer, speech); err != nil {
			return err
		}
		if plan.TerminalType == "dtmf_menu" {
			timeout := cfg.GatherTimeoutSeconds
			if timeout == 0 {
				timeout = 6
			}
			timer := time.NewTimer(time.Duration(timeout) * time.Second)
			defer timer.Stop()
			digit := ""
			for digit == "" {
				select {
				case d := <-digits:
					if strings.Contains(plan.ValidDigits, d) {
						digit = d
					}
				case <-timer.C:
					digit = routingTimeoutSelection
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			// Drain local RTP before a subsequent plan or live bridge takes ownership.
			resumeNode, resumeDigit = plan.NodeID, digit
			return nil
		}
		if plan.TerminalType == "voicemail" {
			beep := make([]int16, 2400)
			for i := range beep {
				beep[i] = int16(4000 * math.Sin(2*math.Pi*1000*float64(i)/24000))
			}
			if err = playSIPPCM(mediaCtx, media, pacer, beep); err != nil {
				return err
			}
			duration := cfg.VoicemailMaxSeconds
			if duration == 0 {
				duration = 120
			}
			recorder, err := a.newLocalRecording(row, true)
			if err != nil {
				return err
			}
			s.recorder.Store(recorder)
			defer func() { s.recorder.CompareAndSwap(recorder, nil); recorder.Close() }()
			timer := time.NewTimer(time.Duration(duration) * time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-digits:
			case <-ctx.Done():
				if s.ctx.Err() == nil {
					return ctx.Err()
				}
			}
			s.recorder.CompareAndSwap(recorder, nil)
			recorder.Close()
			if _, err = a.db().db.Exec(`UPDATE calls SET handling_reason='voicemail',routing_resolution='voicemail' WHERE id=? AND EXISTS(SELECT 1 FROM recordings WHERE id=? AND provider_status='completed' AND duration_ms>0)`, row.ID, recorder.row.ID); err != nil {
				return err
			}

		}
	}
	if s.ended.Load() {
		return errAnswerCallEnded
	}
	fresh, err := a.db().findCall(row.ID)
	if err != nil {
		return err
	}
	if !routingMayControlCall(fresh) {
		return nil
	}
	switch plan.TerminalType {
	case "hangup", "reject", "voicemail":
		return a.endRoutingCall(appCtx, fresh, plan)
	case "destination", "ring_group":
		if plan.AnswerMode == answerModeRealtimeImmediate {
			activateAI = true
			return nil
		}
	}
	return nil
}
func localRoutingSpeechParts(plan *inboundRoutingPlan) []string {
	var ex routingExecutionContext
	_ = json.Unmarshal([]byte(plan.ContextJSON), &ex)
	nodes := map[string]routingNode{}
	for _, n := range ex.Definition.Nodes {
		nodes[n.ID] = n
	}
	parts := []string{}
	for _, step := range plan.Trace {
		if step.NodeType == "announcement" {
			if text := strings.TrimSpace(routingConfigString(nodes[step.NodeID].Config, "text")); text != "" {
				parts = append(parts, text)
			}
		}
	}
	prompt := plan.Prompt
	prefix := strings.Join(parts, " ")
	if prefix != "" && (prompt == prefix || strings.HasPrefix(prompt, prefix+" ")) {
		prompt = strings.TrimSpace(strings.TrimPrefix(prompt, prefix))
	}
	if prompt != "" {
		parts = append(parts, prompt)
	}
	if plan.TerminalType == "voicemail" && prompt == "" {
		parts = append(parts, "Please leave a message after the tone.")
	}

	return parts
}
func playSIPPCM(ctx context.Context, media *sipRTPMedia, pacer *sipRTPPacer, pcm24 []int16) error {
	pcm := newPCMResampler(24000, media.sampleRate()).Process(pcm24)
	// Feed at the playout rate. Long prompts never overflow the bounded queue.
	for offset := 0; offset < len(pcm); {
		if len(pacer.queue) >= 5 {
			select {
			case <-pacer.progress:
			case <-pacer.done:
				return errors.New("SIP playback pacer stopped")
			case <-ctx.Done():
				return ctx.Err()
			case err := <-pacer.Err():
				return err
			}
			continue
		}
		size := media.sampleRate() / 50
		frame := make([]int16, size)
		end := min(len(pcm), offset+size)
		copy(frame, pcm[offset:end])
		if _, err := pacer.enqueue([]sipRTPOutboundPacket{{pcm: frame}}); err != nil {
			return err
		}
		offset = end
	}
	for playback := pacer.playback; playback.hasPending(); {
		select {
		case <-pacer.progress:
		case <-pacer.done:
			return errors.New("SIP playback pacer stopped")
		case <-ctx.Done():
			return ctx.Err()
		case err := <-pacer.Err():
			return err
		}
	}
	return ctx.Err()
}
func (s *sipSession) resumeLocalRouting(ctx *sdk.AppCtx, node, digit string) error {
	a := s.gateway.app
	unlock := lockRoutingCall(s.call.ID)
	defer unlock()
	row, err := a.db().findCall(s.call.ID)
	if err != nil {
		return err
	}
	if !routingMayControlCall(row) {
		return errAnswerCallEnded
	}
	var current string
	if err = a.db().db.QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&current); err != nil {
		return err
	}
	if current != node {
		return nil
	}
	_, plan, err := a.routingPlanForCall(row, map[string]string{node: digit})
	if err != nil {
		return err
	}
	if err = a.updateCallRoutingPlan(row, plan); err != nil {
		return err
	}
	return a.executeSIPRoutingPlan(ctx, row, plan)
}
func (s *sipSession) readLocalRTP(ctx context.Context, m *sipRTPMedia, digits chan<- string, record bool) {
	buffer := make([]byte, 4096)
	var lastEvent uint32
	var haveEvent bool
	resampler := newPCMResampler(m.sampleRate(), 24000)
	for ctx.Err() == nil {
		_ = m.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, source, err := m.readPacket(buffer)
		m.observedReadError(err)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var networkError net.Error
			if !errors.As(err, &networkError) || !networkError.Timeout() {
				return
			}
			continue
		}
		if source.IP.String() != m.remote.IP.String() || source.Port != m.remote.Port {
			m.observations.rejectedSource.Add(1)
			continue
		}
		packet, err := m.decodePacketAt(buffer[:n], m.readAt)
		if err != nil {
			continue
		}
		if int(packet.PayloadType) == m.offer.DTMFPayloadType {
			if len(packet.Payload) >= 4 && packet.Payload[1]&0x80 != 0 && (!haveEvent || packet.Timestamp != lastEvent) {
				d := int(packet.Payload[0])
				if d < 16 {
					lastEvent = packet.Timestamp
					haveEvent = true
					select {
					case digits <- string("0123456789*#ABCD"[d]):
					default:
					}
				}
			}
			continue
		}
		if packet.PayloadType != m.offer.PayloadType {
			continue
		}
		pcm := m.decodeAudio(packet.Payload)
		if record {
			if r := s.recorder.Load(); r != nil {
				r.Record(0, resampler.Process(pcm))
			}
		}
	}
}

func (a *App) toolLocalMediaSettings(caller context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	route := strArg(args, "route_id", "")
	if route != "" {
		if _, err := a.routeForCaller(ctx, route, callerAgentID(caller)); err != nil {
			return mcpError(err.Error()), nil
		}
	} else if callerAgentID(caller) != 0 && args["config"] != nil {
		return mcpError("agents must configure an owned route"), nil
	}
	if boolArg(args, "inherit", false) && route != "" {
		_, err := a.db().db.Exec(`DELETE FROM local_media_settings WHERE project_id=? AND route_id=?`, ctx.CurrentProject(), route)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		a.inventoryReads.invalidate()
		return map[string]any{"ok": true}, nil
	}
	if args["config"] != nil {
		raw, _ := json.Marshal(args["config"])
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var cfg localMediaSettings
		if err := decoder.Decode(&cfg); err != nil {
			return mcpError(err.Error()), nil
		}
		if err := a.saveLocalMediaSettings(ctx, route, cfg); err != nil {
			return mcpError(err.Error()), nil
		}
	}
	cfg, err := a.localMediaSettings(ctx.CurrentProject(), route)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"config": cfg, "applies_to": "new calls", "active_calls_retained": true, "recovery_policy": playbackPolicyName(cfg.RTPPlayback), "legacy_resynchronize_deprecated": true}, nil
}

func (a *App) localSIPPlanRunning(call, node string) bool {
	gateway := a.directSIPGateway()
	if gateway == nil {
		return false
	}
	s := gateway.sessionByCall(call)
	if s == nil {
		return false
	}
	s.local.mu.Lock()
	defer s.local.mu.Unlock()
	if !s.local.plans[node] || s.local.done == nil {
		return false
	}
	select {
	case <-s.local.done:
		return false
	default:
		return true
	}
}
