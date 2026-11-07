package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// This sub-mode is the provider's bridge to Deploy's own execution and hashing
// contract. It never initializes the app, database, gateway, or signing APIs.
func runCloudPipeline(args []string) error {
	if len(args) == 0 {
		return errors.New("expected prepare, finalize or verify")
	}
	mode := args[0]
	flags := flag.NewFlagSet("cloud-pipeline", flag.ContinueOnError)
	source := flags.String("source", "", "capsule root")
	artifact := flags.String("artifact", "", "final signed artifact directory")
	envFile := flags.String("env-file", "", "provider environment file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *source == "" || *artifact == "" {
		return errors.New("source and artifact directories are required")
	}
	body, err := base64.StdEncoding.DecodeString(os.Getenv("APTEVA_BUILD_SPEC_B64"))
	if err != nil || len(body) == 0 {
		return errors.New("missing or invalid APTEVA_BUILD_SPEC_B64")
	}
	var spec runnerBuildSpec
	if err = json.Unmarshal(body, &spec); err != nil {
		return fmt.Errorf("decode build spec: %w", err)
	}
	if subdir, ok := os.LookupEnv("APTEVA_SOURCE_BUILD_SUBDIR"); ok && subdir != spec.BuildSubdir {
		return errors.New("source build subdirectory differs from build spec")
	}
	src, err := pipelinePath(*source, spec.BuildSubdir)
	if err != nil {
		return fmt.Errorf("source build subdirectory: %w", err)
	}
	dist, err := pipelinePath(*artifact, "")
	if err != nil {
		return err
	}
	p, err := pipelineConfig(spec.TargetConfigJSON)
	if err != nil {
		return err
	}
	primary, err := cloudPipelinePrimary(p, spec.TargetKind)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch mode {
	case "prepare":
		native := src
		if p != nil {
			native, err = prepareCommandPipeline(ctx, p, src, dist, spec.Env, os.Stdout)
			if err != nil {
				return err
			}
		}
		if *envFile == "" {
			return errors.New("prepare requires env-file")
		}
		f, err := os.OpenFile(*envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		for _, item := range [][2]string{{"APTEVA_SOURCE_DIR", src}, {"APTEVA_NATIVE_BUILD_DIR", native}, {"APTEVA_OUTPUT_PRIMARY", primary}} {
			if strings.ContainsAny(item[1], "\r\n") {
				return errors.New("provider environment value contains a newline")
			}
			if _, err = fmt.Fprintf(f, "%s=%s\n", item[0], item[1]); err != nil {
				return err
			}
		}
	case "finalize":
		if p != nil {
			return finalizeCommandPipeline(ctx, p, src, dist, spec.Env, os.Stdout)
		}
	case "verify":
		if p != nil {
			manifest, err := readArtifactManifestFile(filepath.Join(dist, artifactManifestFilename))
			if err != nil {
				return err
			}
			return validatePipelineEvidence(p, dist, manifest)
		}
	default:
		return errors.New("expected prepare, finalize or verify")
	}
	return nil
}

func cloudPipelinePrimary(p *commandPipeline, kind string) (string, error) {
	ext := map[string]string{"ios": ".ipa", "macos": ".pkg", "android": ".aab"}[kind]
	if ext == "" {
		return "", fmt.Errorf("unsupported native target %s", kind)
	}
	if p == nil {
		return "app" + ext, nil
	}
	var candidates []string
	for _, output := range p.Outputs {
		if _, err := relativePipelineOutput(output); err != nil {
			return "", err
		}
		if strings.EqualFold(filepath.Ext(output), ext) {
			candidates = append(candidates, output)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("pipeline must declare exactly one %s primary artifact", ext)
	}
	return candidates[0], nil
}

func relativePipelineOutput(output string) (string, error) {
	clean := filepath.Clean(output)
	if output == "" || clean == "." || filepath.IsAbs(output) || strings.ContainsAny(output, "\\\r\n") || clean == ".." || strings.HasPrefix(clean, "../") || clean == artifactManifestFilename {
		return "", fmt.Errorf("invalid pipeline output %q", output)
	}
	return clean, nil
}

func validatePipelineEvidence(p *commandPipeline, dist string, m artifactManifest) error {
	digest, err := artifactTreeDigest(dist)
	if err != nil {
		return err
	}
	if m.Pipeline == nil || m.Pipeline.ArtifactSHA256 != digest || m.Pipeline.ConfigSHA256 != jsonDigest(p) {
		return errors.New("build pipeline evidence is missing or does not match artifact/configuration")
	}
	expected := []string{}
	for _, test := range p.Tests {
		expected = append(expected, test.Name)
	}
	if !slices.Equal(expected, m.Pipeline.Tests) {
		return errors.New("build pipeline test evidence does not match declared tests")
	}
	completed, err := time.Parse(time.RFC3339, m.Pipeline.CompletedAt)
	if err != nil || completed.IsZero() || completed.After(time.Now().Add(5*time.Minute)) {
		return errors.New("build pipeline evidence has invalid completion time")
	}
	if len(p.Outputs) == 0 {
		return errors.New("pipeline must declare artifact outputs")
	}
	for _, output := range p.Outputs {
		if _, err = relativePipelineOutput(output); err != nil {
			return err
		}
		if _, err = pipelinePath(dist, output); err != nil {
			return fmt.Errorf("missing artifact output %s: %w", output, err)
		}
	}
	if m.Primary != "" {
		if !slices.Contains(p.Outputs, m.Primary) {
			return errors.New("artifact manifest primary is not a declared pipeline output")
		}
		path, err := pipelinePath(dist, m.Primary)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("artifact manifest primary must be a file")
		}
	}
	return nil
}

// Preserve the entire artifact tree, including names, modes and safe symlinks.
func importPipelineArchive(path, dist string, p *commandPipeline, configured string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err = unpackZip(&zr.Reader, dist); err != nil {
		return err
	}
	m, err := readArtifactManifestFile(filepath.Join(dist, artifactManifestFilename))
	if err != nil {
		return fmt.Errorf("read pipeline artifact manifest: %w", err)
	}
	if m.Primary == "" {
		return errors.New("pipeline native artifact manifest requires primary")
	}
	if configured != "" && configured != m.Primary {
		return errors.New("artifact_file differs from pipeline manifest primary")
	}
	return validatePipelineEvidence(p, dist, m)
}
