// Benchmarks of the store and of what a redirect does around it: lookups in
// a store of 2,000,000 links (and of 20,000,000 with QISQA_BENCH_LARGE=1),
// the same with the host table switched off, durable creates, the disk a
// link takes, turning a code into its number and a hit in the link cache.
// Nothing here runs without -bench. Each store is built once per run, in a
// temporary folder, so TMPDIR decides which disk is measured.
package main

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// benchDomain is the domain the benchmark links are stored on, and
// benchLength the length of their codes: the default min_length.
const (
	benchDomain = 1
	benchLength = 5
)

// benchURL returns a URL of the shape the benchmarks store: on example.com,
// with 25 random characters.
func benchURL(rnd *rand.Rand) string {
	var name [25]byte
	for i := range name {
		name[i] = alphabet[rnd.IntN(len(alphabet))]
	}
	return "https://example.com/t/" + string(name[:])
}

// benchLinks fills links with new links to benchURLs that expire in a year.
func benchLinks(rnd *rand.Rand, links []NewLink) {
	expires := time.Now().AddDate(1, 0, 0)
	for i := range links {
		links[i] = NewLink{URL: benchURL(rnd), ExpiresAt: expires}
	}
}

// perSecond reports how many operations a second the benchmark did.
func perSecond(b *testing.B, unit string) {
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), unit)
}

// benchStore fills a store in a temporary folder with n links and returns
// it opened again, as qisqa finds it after a restart, with 1,048,576 codes
// picked at random among those links. The links are written without a sync
// each, a chunk at a time, and every file is synced once at the end, so that
// no write is still on its way to the disk while lookups are measured.
func benchStore(b *testing.B, n int) (*Store, []string) {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "data")
	st, err := OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	st.noSync = true
	rnd := rand.New(rand.NewPCG(1, 2))
	links := make([]NewLink, chunkLinks)
	for done := 0; done < n; {
		batch := links[:min(chunkLinks, n-done)]
		benchLinks(rnd, batch)
		if _, err := st.Create(benchDomain, 1, benchLength, anyCode, batch); err != nil {
			b.Fatal(err)
		}
		done += len(batch)
	}
	if err := st.Close(); err != nil {
		b.Fatal(err)
	}
	err = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
	if err != nil {
		b.Fatal(err)
	}

	st, err = OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	coder := NewCoder(st.key, benchDomain)
	codes := make([]string, 1<<20)
	for i := range codes {
		codes[i] = coder.Code(benchLength, rnd.Uint64N(uint64(n)))
	}
	return st, codes
}

// benchLookups measures lookups of codes in st: by one goroutine, and by as
// many goroutines as -cpu says, all at once.
func benchLookups(b *testing.B, st *Store, codes []string) {
	b.Run("goroutines=1", func(b *testing.B) {
		b.ReportAllocs()
		rnd := rand.New(rand.NewPCG(3, 4))
		for b.Loop() {
			code := codes[rnd.IntN(len(codes))]
			if _, state, err := st.Lookup(benchDomain, code); err != nil || state != LinkFound {
				b.Fatalf("lookup of %s: %v, %v", code, state, err)
			}
		}
		perSecond(b, "lookups/s")
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		var seed atomic.Uint64
		b.RunParallel(func(pb *testing.PB) {
			rnd := rand.New(rand.NewPCG(seed.Add(1), 5))
			for pb.Next() {
				code := codes[rnd.IntN(len(codes))]
				if _, state, err := st.Lookup(benchDomain, code); err != nil || state != LinkFound {
					b.Errorf("lookup of %s: %v, %v", code, state, err)
					return
				}
			}
		})
		perSecond(b, "lookups/s")
	})
}

// BenchmarkLookup measures lookups of random existing codes, the work of a
// redirect the link cache does not answer.
func BenchmarkLookup(b *testing.B) {
	sizes := []int{2_000_000}
	if os.Getenv("QISQA_BENCH_LARGE") == "1" {
		sizes = append(sizes, 20_000_000)
	}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("links=%dM", n/1_000_000), func(b *testing.B) {
			st, codes := benchStore(b, n)
			benchLookups(b, st, codes)
		})
	}
}

// BenchmarkLookupInlineHosts is BenchmarkLookup at 2,000,000 links with the
// host table switched off, so that every record holds its host. Lookups
// with the table may be at most 5% slower than these.
func BenchmarkLookupInlineHosts(b *testing.B) {
	b.Run("links=2M", func(b *testing.B) {
		defer func(n int) { maxHosts = n }(maxHosts)
		maxHosts = 0
		st, codes := benchStore(b, 2_000_000)
		benchLookups(b, st, codes)
	})
}

// BenchmarkCreate measures creates of one link a call, with real syncs, by
// one writer and by 64 at once, with sync_every 0, where a create returns
// once it is synced, and with 1s, where it returns at once and is synced
// within the second. Creates that run together share a sync, and
// creates/sync says how many did.
func BenchmarkCreate(b *testing.B) {
	for _, every := range []time.Duration{0, time.Second} {
		for _, writers := range []int{1, 64} {
			b.Run(fmt.Sprintf("sync_every=%v/writers=%d", every, writers), func(b *testing.B) {
				benchCreate(b, every, writers)
			})
		}
	}
}

// benchCreate is one row of BenchmarkCreate.
func benchCreate(b *testing.B, every time.Duration, writers int) {
	st, err := OpenStore(filepath.Join(b.TempDir(), "data"))
	if err != nil {
		b.Fatal(err)
	}
	st.syncEvery = every
	defer st.Close()
	create := func(rnd *rand.Rand) error {
		link := make([]NewLink, 1)
		benchLinks(rnd, link)
		_, err := st.Create(benchDomain, 1, benchLength, anyCode, link)
		return err
	}
	// The first link starts the domain and its chunk.
	if err := create(rand.New(rand.NewPCG(0, 0))); err != nil {
		b.Fatal(err)
	}
	var left atomic.Int64
	left.Store(int64(b.N))
	syncs := st.syncs()
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			rnd := rand.New(rand.NewPCG(uint64(w), 1))
			for left.Add(-1) >= 0 {
				if err := create(rnd); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	b.StopTimer()
	perSecond(b, "creates/s")
	if syncs = st.syncs() - syncs; syncs > 0 {
		b.ReportMetric(float64(b.N)/float64(syncs), "creates/sync")
	}
}

// BenchmarkCreateChunk measures opening a new store and filling its first
// chunk in one call to Create, 65,536 links with one sync of the chunk
// file, and reports the disk that file then takes per link: the space
// allocated to it, which du counts, rather than its length.
func BenchmarkCreateChunk(b *testing.B) {
	rnd := rand.New(rand.NewPCG(1, 2))
	links := make([]NewLink, chunkLinks)
	var allocated int64
	for b.Loop() {
		b.StopTimer()
		benchLinks(rnd, links)
		dir, err := os.MkdirTemp("", "qisqa-bench")
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		st, err := OpenStore(dir)
		if err == nil {
			_, err = st.Create(benchDomain, 1, benchLength, anyCode, links)
		}
		if err == nil {
			err = st.Close()
		}
		b.StopTimer()
		var info syscall.Stat_t
		if err == nil {
			err = syscall.Stat(filepath.Join(dir, "d1", chunkName(benchLength, 0)), &info)
		}
		os.RemoveAll(dir)
		if err != nil {
			b.Fatal(err)
		}
		allocated = info.Blocks * 512 // st_blocks counts 512-byte units
		b.StartTimer()
	}
	b.ReportMetric(float64(allocated)/chunkLinks, "bytes/link")
}

// BenchmarkCode measures turning a code back into its link number, the
// first step of every lookup.
func BenchmarkCode(b *testing.B) {
	coder := NewCoder(make([]byte, keySize), benchDomain)
	rnd := rand.New(rand.NewPCG(1, 2))
	codes := make([]string, 1<<16)
	for i := range codes {
		codes[i] = coder.Code(benchLength, rnd.Uint64N(classCapacity(benchLength)))
	}
	i := 0
	for b.Loop() {
		if _, _, ok := coder.Number(codes[i%len(codes)]); !ok {
			b.Fatalf("%q is not a code", codes[i%len(codes)])
		}
		i++
	}
}

// BenchmarkCacheGet measures a hit in a full link cache of the default
// size, what a redirect of a cached link costs instead of a lookup.
func BenchmarkCacheGet(b *testing.B) {
	const size = 10_000 // the default cache.max_entries
	cache := NewCache(size)
	coder := NewCoder(make([]byte, keySize), benchDomain)
	rnd := rand.New(rand.NewPCG(1, 2))
	keys := make([]string, size)
	expires := time.Now().Add(24 * time.Hour)
	for i := range keys {
		keys[i] = cacheKey("short.example", coder.Code(benchLength, uint64(i)))
		cache.Set(keys[i], benchURL(rnd), expires)
	}
	if cache.Len() != size {
		b.Fatalf("the cache holds %d entries, want %d", cache.Len(), size)
	}
	order := rnd.Perm(size)
	now := time.Now()
	i := 0
	for b.Loop() {
		if _, ok := cache.Get(keys[order[i%size]], now); !ok {
			b.Fatalf("%s is not in the cache", keys[order[i%size]])
		}
		i++
	}
}
