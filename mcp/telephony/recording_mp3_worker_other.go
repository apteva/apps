//go:build !unix

package main

func lowerRecordingMP3WorkerPriority() error { return nil }

func recordingMP3WorkerThreadNice() (int, bool) { return 0, false }
