package browserbase

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/som"
	"github.com/chromedp/chromedp"
)

func TestBrowserbaseUploadFileFromSOMLabelOnDataTrigger(t *testing.T) {
	if os.Getenv("RUN_BROWSERBASE_LABEL_UPLOAD_TESTS") == "" {
		t.Skip("set RUN_BROWSERBASE_LABEL_UPLOAD_TESTS=1")
	}
	runBrowserbaseUploadFileFromSOMLabelOnDataTrigger(t, "https://seleniumbase.io/apps/img_upload")
}

func TestBrowserbaseUploadFileFromSOMLabelOnImgBB(t *testing.T) {
	if os.Getenv("RUN_BROWSERBASE_IMGBB_UPLOAD_TESTS") == "" {
		t.Skip("set RUN_BROWSERBASE_IMGBB_UPLOAD_TESTS=1")
	}
	runBrowserbaseUploadFileFromSOMLabelOnDataTrigger(t, "https://imgbb.com/")
}

func runBrowserbaseUploadFileFromSOMLabelOnDataTrigger(t *testing.T, uploadPage string) {
	t.Helper()
	apiKey := os.Getenv("BROWSERBASE_API_KEY")
	projectID := os.Getenv("BROWSERBASE_PROJECT_ID")
	if apiKey == "" || projectID == "" {
		t.Fatal("BROWSERBASE_API_KEY and BROWSERBASE_PROJECT_ID required")
	}

	sourceURL := strings.TrimSpace(os.Getenv("BROWSERBASE_LABEL_UPLOAD_SOURCE_URL"))
	if sourceURL == "" {
		sourceURL = "https://the-internet.herokuapp.com/img/forkme_right_green_007200.png"
	}
	filename := filepath.Base(strings.Split(sourceURL, "?")[0])
	if envFilename := strings.TrimSpace(os.Getenv("BROWSERBASE_LABEL_UPLOAD_FILENAME")); envFilename != "" {
		filename = envFilename
	}
	if filename == "" || filename == "." || filename == "/" {
		filename = "browserbase-label-upload.png"
	}

	filePath := filepath.Join(t.TempDir(), filename)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sourceURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("download source image: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("download source image: HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prepared file=%s size=%d", filePath, info.Size())

	c, err := New(apiKey, projectID, computer.DisplaySize{Width: 1440, Height: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.OpenSession(computer.OpenOptions{URL: uploadPage}); err != nil {
		t.Fatal(err)
	}

	mode := strings.TrimSpace(os.Getenv("BROWSERBASE_LABEL_UPLOAD_MODE"))
	if mode == "" {
		mode = "label"
	}
	if _, err := c.ScreenshotWithOptions(computer.ScreenshotOptions{Annotate: true}); err != nil {
		t.Fatalf("annotated screenshot: %v", err)
	}

	action := computer.Action{
		Type:  "upload_file",
		Files: []string{filePath},
	}
	if mode == "selector" {
		action.Selector = "#anywhere-upload-input"
		t.Logf("using selector %s", action.Selector)
	} else {
		labels := make([]int, 0, len(c.lastLabels))
		for label := range c.lastLabels {
			labels = append(labels, label)
		}
		sort.Ints(labels)
		for _, label := range labels {
			e := c.lastLabels[label]
			t.Logf("som label=%d tag=%s role=%s type=%s text=%q box=%d,%d %dx%d", label, e.Tag, e.Role, e.Type, e.Text, e.X, e.Y, e.W, e.H)
		}

		targetLabel := bestUploadLabel(c.lastLabels, labels)
		if targetLabel == 0 {
			t.Fatal("no SOM upload label found")
		}
		action.Label = targetLabel
		t.Logf("using target label %d", targetLabel)
	}

	if _, err := c.Execute(action); err != nil {
		t.Fatalf("upload_file mode=%s: %v", mode, err)
	}
	time.Sleep(2 * time.Second)

	var state map[string]any
	js := `(() => {
  const input = document.querySelector('#anywhere-upload-input');
  const file = input && input.files && input.files[0];
  const queue = window.CHV && CHV.obj && CHV.obj.uploader && CHV.obj.uploader.queue;
  const root = document.querySelector('.upload-box');
  const dialogs = Array.from(document.querySelectorAll('[role=dialog], .modal, .toast, .bootbox'))
    .map((el) => (el.innerText || '').trim()).filter(Boolean);
  return {
    inputFiles: input && input.files ? input.files.length : -1,
    inputFileName: file ? file.name : '',
    inputFileSize: file ? file.size : -1,
    inputFileType: file ? file.type : '',
    queueSize: Array.isArray(queue) ? queue.length : -1,
    rootClass: root ? root.className : '',
    dialogs: dialogs,
    bodyHead: (document.body.innerText || '').slice(0, 300)
  };
})()`
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(js, &state)); err != nil {
		t.Fatalf("inspect final DOM: %v", err)
	}
	t.Logf("final state: %#v", state)
	if got := int(state["inputFiles"].(float64)); got != 1 {
		t.Fatalf("inputFiles=%d, want 1", got)
	}
	if got := state["inputFileName"].(string); got != filename {
		t.Fatalf("inputFileName=%q, want %q", got, filename)
	}
	rootClass, _ := state["rootClass"].(string)
	if !strings.Contains(rootClass, "queueReady") {
		t.Fatalf("site did not enter queueReady after file selection; rootClass=%q bodyHead=%q", rootClass, state["bodyHead"])
	}
}

func bestUploadLabel(labels map[int]som.Element, ordered []int) int {
	bestLabel := 0
	bestScore := 0
	for _, label := range ordered {
		e := labels[label]
		text := strings.ToLower(e.Text)
		score := 0
		switch {
		case strings.Contains(text, "browse"):
			score = 100
		case strings.Contains(text, "start uploading"):
			score = 90
		case strings.Contains(text, "drag and drop"):
			score = 80
		case strings.Contains(text, "upload"):
			score = 70
		case e.Tag == "span" && e.W >= 80 && e.H >= 40:
			score = 30
		}
		if score > bestScore {
			bestScore = score
			bestLabel = label
		}
	}
	return bestLabel
}
