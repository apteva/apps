package main

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
)

const monika9564Frame490745SHA256 = "527fb8f69f9b694036c6de1917cff690b1326d6f13c5d89fa51ebf2890ca8cd7"

// TestMonika9564Frame490745LocalRegression is an opt-in real-image regression
// for the production frame that exposed a reclining face just outside the
// initially selected portrait crop. CI remains self-contained. Run with:
//
//	MONIKA_9564_FRAME_490745=/path/to/frame.png go test -run TestMonika9564Frame490745LocalRegression -v
func TestMonika9564Frame490745LocalRegression(t *testing.T) {
	path := os.Getenv("MONIKA_9564_FRAME_490745")
	if path == "" {
		t.Skip("set MONIKA_9564_FRAME_490745 to run the real-image regression")
	}
	assertFileSHA256(t, path, monika9564Frame490745SHA256)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	win, err := analyzeSmartCropV2Frame(1920, 1080, 9, 16, img)
	if err != nil {
		t.Fatal(err)
	}
	// Monika's face spans roughly source x=680..850. Keeping the crop at or
	// left of 650 preserves her whole face with a useful safety margin.
	if win.X > 650 {
		t.Fatalf("reclining face remains outside portrait crop: x=%d want<=650", win.X)
	}
	if win.X < 400 {
		t.Fatalf("recovery overcorrected away from the reclining subject: x=%d want>=400", win.X)
	}
	t.Logf("exact frame crop x=%d", win.X)
}

// TestMonika9564Storyboard490745LocalRegression reproduces the full production
// still decision: nine cached storyboard frames, focus interpolation, temporal
// consensus, and the final head guard. This is the path that originally chose
// x=906 even though Monika's reclining face was immediately outside the crop.
func TestMonika9564Storyboard490745LocalRegression(t *testing.T) {
	dir := os.Getenv("MONIKA_9564_STORYBOARD_DIR")
	if dir == "" {
		t.Skip("set MONIKA_9564_STORYBOARD_DIR to run the production-path regression")
	}
	fixtures := []struct {
		name   string
		atMs   int64
		sha256 string
	}{
		{"9643.jpg", 467_620, "297f76e83b9f6aefca2322a65262cfe554f0ffd6ea7d9e3387b7d309802f9640"},
		{"9644.jpg", 473_680, "994e4ac35044461a680b2b9a16a6ed6127d5821eecfa66e27ca7562fc13a712d"},
		{"9645.jpg", 479_740, "33321a09af54fc587ab4ab90259c262742c99914647576040a063d93cc01bec9"},
		{"9646.jpg", 485_800, "5872084c11dab84ab7a99b0e65c22a2c4c5ac9bdfbefd3453921d766f90335d1"},
		{"9647.jpg", 491_860, "7fb094db292d51387e345b2c5b728106db46138034971cf09b5cf30ab61a6549"},
		{"9648.jpg", 497_920, "86b94098797ee646ca1d42a5f29bd6abbaaac528fd3251786ac220a7ae24a4eb"},
		{"9649.jpg", 503_980, "610dcc607e85e0c9f2a02ef41b6853a490bb690fcca0d5f9a0177102cb44d2f4"},
		{"9650.jpg", 510_040, "d9db8803fef58a3c0f0f344bb512d7d2b4aef7f10cb0fe3480b44bcf24ca215c"},
		{"9651.jpg", 516_100, "ef1d25517684817dec0fd636bb6232dcf9db321ced90cdc8a98cc7d993549040"},
	}
	samples := make([]smartCropV2Sample, 0, len(fixtures))
	for _, fixture := range fixtures {
		path := filepath.Join(dir, fixture.name)
		assertFileSHA256(t, path, fixture.sha256)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		img, _, decodeErr := image.Decode(f)
		f.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		win, err := analyzeSmartCropV2Frame(1920, 1080, 9, 16, img)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: fixture.atMs, X: win.X},
			img:   img,
		})
	}

	const focusMs = int64(490_745)
	cropW, _ := cropDimsForRatio(1920, 1080, 9, 16)
	x, _, err := resolveSmartCropStillBase(samples, focusMs)
	if err != nil {
		t.Fatal(err)
	}
	scene := smartCropSceneSamples(samples, focusMs)
	result, ok := temporalSubjectConsensus(scene, 1920, cropW)
	if !ok {
		result, ok = staticWarmSubjectConsensus(scene, 1920, cropW)
	}
	if ok {
		if corrected, changed := applySmartCropTemporalOverride(x, result, cropW, 1920); changed {
			x = corrected
		}
	}
	if sample := nearestSmartCropSample(samples, focusMs); sample != nil {
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, 1920, cropW); changed {
			x = corrected
		}
	}
	if x > 650 {
		t.Fatalf("production path leaves reclining face outside crop: x=%d want<=650", x)
	}
	if x < 400 {
		t.Fatalf("production path overcorrected away from subject: x=%d want>=400", x)
	}
	t.Logf("production storyboard crop x=%d", x)
}
