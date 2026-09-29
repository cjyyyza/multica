package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	verifyPublishedForkMigrations(t)
	var stems []string
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if ok {
			stems = append(stems, stem)
		}
	}
	for _, conflict := range migrationNumberConflicts(stems) {
		t.Error(conflict)
	}
}

func migrationNumberConflicts(stems []string) []string {
	// Upstream migrations through 128 contain historical duplicate numbers.
	// The separately published fork history has exact names and SQL checked
	// above. Neither historical exception may admit a new collision.
	const firstUniqueMigrationNumber = 129
	stemByNumber := make(map[int]string)
	for version := range publishedForkMigrationDigests {
		prefix, _, _ := strings.Cut(version, "_")
		number, _ := strconv.Atoi(prefix)
		stemByNumber[number] = "published fork history"
	}
	var conflicts []string
	for _, stem := range stems {
		if _, published := publishedForkMigrationDigests[stem]; published {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		if previous, exists := stemByNumber[number]; exists &&
			!(previous == "published fork history" && upstreamVersionsSharingForkNumbers[number] == stem) {
			conflicts = append(conflicts, fmt.Sprintf("migrations %s and %s share numeric prefix %s", previous, stem, prefix))
			continue
		}
		stemByNumber[number] = stem
	}
	return conflicts
}

func TestMigrationNumberHistoryDoesNotAdmitNewCollisions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stems         []string
		wantConflicts int
	}{
		{"published collision", []string{"500_issue_origin_popo_chat", "500_workspace_p4_depots", "500_task_message_call_id"}, 0},
		{"new fork collision", []string{"500_new_feature"}, 1},
		{"new collision beside upstream", []string{"500_task_message_call_id", "500_new_feature"}, 1},
		{"new ordinary collision", []string{"900_first", "900_second"}, 1},
		{"new unique migration", []string{"900_first"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := migrationNumberConflicts(tc.stems); len(got) != tc.wantConflicts {
				t.Fatalf("conflicts = %v, want %d", got, tc.wantConflicts)
			}
		})
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
