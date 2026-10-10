//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pion/opus"
)

// Opt-in codec capacity measurement. Each simulated call owns the production
// encoder, resampler and decoder and runs at 50 frames/s. This does not include
// SRTP, sockets, routing or database work and is not a deployment capacity limit.
func TestRTCCodecCPUStress(t *testing.T) {
	output, source := os.Getenv("TELEPHONY_CPU_OUTPUT"), os.Getenv("TELEPHONY_SPEECH_INPUT")
	if output == "" || source == "" {
		t.Skip("local codec CPU stress is opt-in")
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := inspectPCM16WAV(file)
	if err != nil || info.SampleRate != 24000 || info.Channels != 1 {
		t.Fatal("24 kHz mono PCM speech required", err)
	}
	var frames [][]int16
	var pending []int16
	err = forEachWAVFrame(file, info, func(pcm []int16) error {
		pending = append(pending, pcm...)
		for len(pending) >= 480 {
			frames = append(frames, append([]int16(nil), pending[:480]...))
			pending = pending[480:]
		}
		return nil
	})
	if err != nil || len(frames) < 100 {
		t.Fatal("speech input too short", err)
	}
	// One fixed ingress speech stream keeps decoder inputs identical across
	// all variants. Initialization and corpus encoding are outside measurements.
	e, err := newRTCNativeEncoder(32000)
	if err != nil {
		t.Fatal("native FEC must be available for this measurement", err)
	}
	var packets [][]byte
	r := newPCMResampler(24000, 48000)
	var samples []float32
	buf := make([]byte, 1275)
	for _, frame := range frames {
		for _, v := range r.Process(frame) {
			samples = append(samples, float32(v)/32768)
		}
		if len(samples) < 960 {
			continue
		}
		n, err := e.EncodeFloat32(samples[:960], buf)
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, append([]byte(nil), buf[:n]...))
		samples = samples[960:]
	}
	e.Close()
	frames = frames[:len(packets)]
	var results []rtcCPUResult
	for _, calls := range []int{1, 10, 25, 50} {
		for _, variant := range []string{"portable", "native_no_fec", "native_fec", "native_fec_5pct_loss"} {
			result, err := measureRTCCodecCPU(variant, calls, 3*time.Second, frames, packets)
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, result)
			t.Logf("%s: %d calls, %.1f%% one CPU, %.3f%% one CPU/call, work p99 %.2fms, skipped slots %d/%d", variant, calls, result.ProcessCPUPercent, result.ProcessCPUPercent/float64(calls), result.WorkP99MS, result.SkippedSlots, result.CompletedFrames+result.SkippedSlots)
		}
	}
	report := map[string]any{"scope": "real-time paced production codecs + resampling + decoding; excludes sockets/SRTP/routing/database", "source": "local French speech", "gomaxprocs": runtime.GOMAXPROCS(0), "logical_cpus": runtime.NumCPU(), "results": results}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, raw, 0644); err != nil {
		t.Fatal(err)
	}
}

type rtcCPUResult struct {
	Variant           string  `json:"variant"`
	Calls             int     `json:"calls"`
	WallSeconds       float64 `json:"wall_seconds"`
	ProcessCPUSeconds float64 `json:"process_cpu_seconds"`
	ProcessCPUPercent float64 `json:"process_cpu_percent_of_one_core"`
	CPUPerFrameUS     float64 `json:"process_cpu_us_per_completed_frame"`
	ProjectedCPU      float64 `json:"projected_cpu_percent_at_50_frames_per_call_second"`
	CompletedFrames   int     `json:"completed_frames"`
	SkippedSlots      int     `json:"skipped_20ms_slots"`
	WorkP50MS         float64 `json:"work_p50_ms"`
	WorkP99MS         float64 `json:"work_p99_ms"`
	WorkMaxMS         float64 `json:"work_max_ms"`
	AllocatedBytes    uint64  `json:"allocated_bytes"`
	Allocations       uint64  `json:"allocations"`
}

func rtcProcessCPUSeconds() (float64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6, nil
}

func measureRTCCodecCPU(variant string, calls int, duration time.Duration, frames [][]int16, packets [][]byte) (rtcCPUResult, error) {
	type worker struct {
		encoder   rtcOpusEncoder
		decoder   rtcOpusDecoder
		resampler *pcmResampler
		costs     []float64
		skipped   int
		err       error
	}
	workers := make([]worker, calls)
	for i := range workers {
		w := &workers[i]
		var err error
		if variant == "portable" {
			w.encoder, err = newRTCGoEncoder(32000)
			if err == nil {
				d, derr := opus.NewDecoderWithOutput(24000, 1)
				err = derr
				if err == nil {
					w.decoder = &rtcGoOpusDecoder{&d}
				}
			}
		} else {
			w.encoder, err = newRTCNativeEncoder(32000)
			if err == nil && variant == "native_no_fec" {
				native := w.encoder.(*rtcNativeEncoder)
				if native.lib.set(native.state, 4012, 0) != 0 {
					err = fmt.Errorf("cannot disable FEC for control")
				}
			}
			if err == nil {
				w.decoder, err = newRTCNativeDecoder()
			}
		}
		if w.encoder != nil {
			defer w.encoder.Close()
		}
		if w.decoder != nil {
			defer w.decoder.Close()
		}
		if err != nil {
			return rtcCPUResult{}, err
		}
		w.resampler = newPCMResampler(24000, 48000)
		w.costs = make([]float64, 0, int(duration/(20*time.Millisecond))+1)
	}
	var group sync.WaitGroup
	startSignal := make(chan struct{})
	var start time.Time // Published to workers through startSignal.
	for i := range workers {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-startSignal
			w := &workers[i]
			pcm, out := make([]float32, 0, 1920), make([]byte, 1275)
			decoded := make([]int16, 2880)
			base := start.Add(time.Duration(i%20) * time.Millisecond)
			slot := 0
			previousMissing := false
			for {
				expected := base.Add(time.Duration(slot) * 20 * time.Millisecond)
				if !expected.Before(base.Add(duration)) {
					return
				}
				if wait := time.Until(expected); wait > 0 {
					time.Sleep(wait)
				}
				actual := int(time.Since(base) / (20 * time.Millisecond))
				if actual > slot {
					w.skipped += actual - slot
					slot = actual
				}
				if time.Since(base) >= duration {
					return
				}
				index := (slot + i*13) % len(frames)
				at := time.Now()
				for _, v := range w.resampler.Process(frames[index]) {
					pcm = append(pcm, float32(v)/32768)
				}
				if len(pcm) >= 960 {
					_, w.err = w.encoder.EncodeFloat32(pcm[:960], out)
					copy(pcm, pcm[960:])
					pcm = pcm[:len(pcm)-960]
					if w.err != nil {
						return
					}
				}
				missing := variant == "native_fec_5pct_loss" && slot%20 == 10
				if !missing {
					if previousMissing {
						_, w.err = w.decoder.recover(packets[index], decoded[:480], true)
					}
					if w.err == nil {
						_, w.err = w.decoder.decode(packets[index], decoded)
					}
					if w.err != nil {
						return
					}
				}
				previousMissing = missing
				w.costs = append(w.costs, float64(time.Since(at))/float64(time.Millisecond))
				slot++
			}
		}(i)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpuStart, err := rtcProcessCPUSeconds()
	if err != nil {
		start = time.Now()
		close(startSignal)
		group.Wait()
		return rtcCPUResult{}, err
	}
	at := time.Now()
	start = at.Add(20 * time.Millisecond)
	close(startSignal)
	group.Wait()
	wall := time.Since(at).Seconds()
	cpuEnd, err := rtcProcessCPUSeconds()
	if err != nil {
		return rtcCPUResult{}, err
	}
	runtime.ReadMemStats(&after)
	result := rtcCPUResult{Variant: variant, Calls: calls, WallSeconds: wall, ProcessCPUSeconds: cpuEnd - cpuStart, AllocatedBytes: after.TotalAlloc - before.TotalAlloc, Allocations: after.Mallocs - before.Mallocs}
	result.ProcessCPUPercent = result.ProcessCPUSeconds / wall * 100
	var costs []float64
	for _, w := range workers {
		if w.err != nil {
			return result, w.err
		}
		result.SkippedSlots += w.skipped
		costs = append(costs, w.costs...)
	}
	slices.Sort(costs)
	result.CompletedFrames = len(costs)
	if len(costs) > 0 {
		// Do not report a lower CPU requirement just because the host failed
		// to schedule all requested slots. Keep misses and projections separate.
		result.CPUPerFrameUS = result.ProcessCPUSeconds * 1e6 / float64(len(costs))
		result.ProjectedCPU = result.CPUPerFrameUS * 50 * float64(calls) / 1e6 * 100
		result.WorkP50MS = costs[len(costs)/2]
		result.WorkP99MS = costs[(len(costs)-1)*99/100]
		result.WorkMaxMS = costs[len(costs)-1]
	}
	return result, nil
}
