package mteb

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// nfcorpusBaseURL is the HuggingFace mirror for the NFCorpus BEIR
// distribution. Kept as a package var (not const) so tests can stub it
// out — production code never reassigns.
var nfcorpusBaseURL = "https://huggingface.co/datasets/mteb/nfcorpus/resolve/main"

// Doc is one corpus document. Title + Text both feed into the embedding
// for indexing; downstream metrics only need the ID.
type Doc struct {
	ID    string
	Title string
	Text  string
}

// Body returns the text the embedder should index for this doc.
// Concatenates Title + Text with a separator that won't collide with
// real content. NFCorpus titles are short and informative — keep them.
func (d Doc) Body() string {
	if d.Title == "" {
		return d.Text
	}
	return d.Title + ". " + d.Text
}

// Query is one MTEB retrieval query.
type Query struct {
	ID   string
	Text string
}

// Task is the resolved per-task dataset: corpus + queries + qrels.
// Wrapped in benchmarks.Instance.Payload so runner.Run can find it.
type Task struct {
	Name    string
	Corpus  map[string]Doc
	Queries []Query
	Qrels   map[string]map[string]int // query_id → corpus_id → relevance grade
}

// loadNFCorpus fetches the three NFCorpus files via the skeleton's
// EnsureCached layer and returns a fully-populated *Task.
//
// The `mteb/nfcorpus` dataset on HuggingFace ships uncompressed JSONL
// + TSV (not .gz). Disk overhead is ~6 MB compared to ~2 MB gzipped —
// trivial. Parsers stream line-by-line either way.
func loadNFCorpus(ctx context.Context) (*Task, error) {
	corpusPath, err := benchmarks.EnsureCached("mteb", "nfcorpus/corpus.jsonl",
		nfcorpusBaseURL+"/corpus.jsonl")
	if err != nil {
		return nil, fmt.Errorf("fetch corpus: %w", err)
	}
	queriesPath, err := benchmarks.EnsureCached("mteb", "nfcorpus/queries.jsonl",
		nfcorpusBaseURL+"/queries.jsonl")
	if err != nil {
		return nil, fmt.Errorf("fetch queries: %w", err)
	}
	qrelsPath, err := benchmarks.EnsureCached("mteb", "nfcorpus/qrels/test.tsv",
		nfcorpusBaseURL+"/qrels/test.tsv")
	if err != nil {
		return nil, fmt.Errorf("fetch qrels: %w", err)
	}

	corpus, err := parseCorpusFile(corpusPath)
	if err != nil {
		return nil, fmt.Errorf("parse corpus: %w", err)
	}
	queries, err := parseQueriesFile(queriesPath)
	if err != nil {
		return nil, fmt.Errorf("parse queries: %w", err)
	}
	qrels, err := parseQrelsFile(qrelsPath)
	if err != nil {
		return nil, fmt.Errorf("parse qrels: %w", err)
	}

	return &Task{
		Name:    "NFCorpus",
		Corpus:  corpus,
		Queries: queries,
		Qrels:   qrels,
	}, nil
}

// openDataReader transparently handles .gz files (legacy BEIR layout) and
// plain text (the current mteb/nfcorpus layout). Streams in both cases.
func openDataReader(path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from EnsureCached
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &gzReadCloser{f: f, gz: gz}, nil
}

type gzReadCloser struct {
	f  *os.File
	gz *gzip.Reader
}

func (r *gzReadCloser) Read(p []byte) (int, error) { return r.gz.Read(p) }

func (r *gzReadCloser) Close() error {
	if err := r.gz.Close(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}

func parseCorpusFile(path string) (map[string]Doc, error) {
	rc, err := openDataReader(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	corpus := make(map[string]Doc, 4096)
	scan := bufio.NewScanner(rc)
	// NFCorpus docs are short but a few exceed bufio.MaxScanTokenSize
	// (64KiB). Lift the buffer so the scan doesn't fail mid-corpus.
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			ID    string `json:"_id"`
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("corpus line: %w", err)
		}
		if rec.ID == "" {
			continue
		}
		corpus[rec.ID] = Doc{ID: rec.ID, Title: rec.Title, Text: rec.Text}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return corpus, nil
}

func parseQueriesFile(path string) ([]Query, error) {
	rc, err := openDataReader(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var queries []Query
	scan := bufio.NewScanner(rc)
	scan.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			ID   string `json:"_id"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("query line: %w", err)
		}
		if rec.ID == "" {
			continue
		}
		queries = append(queries, Query{ID: rec.ID, Text: rec.Text})
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return queries, nil
}

func parseQrelsFile(path string) (map[string]map[string]int, error) {
	rc, err := openDataReader(path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	qrels := make(map[string]map[string]int, 1024)
	scan := bufio.NewScanner(rc)
	scan.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	first := true
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			// BEIR qrels TSVs start with a header row.
			if strings.HasPrefix(line, "query-id") {
				continue
			}
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, fmt.Errorf("malformed qrels line: %q", line)
		}
		qid, did, scoreStr := fields[0], fields[1], fields[2]
		score, err := strconv.Atoi(scoreStr)
		if err != nil {
			return nil, fmt.Errorf("qrels score %q: %w", scoreStr, err)
		}
		if qrels[qid] == nil {
			qrels[qid] = map[string]int{}
		}
		qrels[qid][did] = score
	}
	return qrels, scan.Err()
}
