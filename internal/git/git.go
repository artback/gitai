package git

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	gitssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/sergi/go-diff/diffmatchpatch"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Constants for safety and consistency
const (
	MaxDiffSize     = 500 * 1024 // 500 KB limit for diff generation
	BinaryScanLimit = 8000       // Bytes to scan for null-byte binary detection
)

// ErrOutsideRepo is returned when a provided path is not within the git repository.
var ErrOutsideRepo = errors.New("path is outside the repository")

// Service provides methods for interacting with a Git repository.
type Service struct{}

// NewService creates a new Service instance.
func NewService() *Service {
	return &Service{}
}

// repoContext holds state for a single operation to avoid redundant disk I/O
type repoContext struct {
	repo     *git.Repository
	worktree *git.Worktree
	root     string
	head     *object.Commit // Nil if initial commit
}

// --- Public API Methods ---

// GetStatusForFiles returns the porcelain status of the specified files.
func (s *Service) GetStatusForFiles(files []string) (string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}

	status, err := ctx.worktree.Status()
	if err != nil {
		return "", fmt.Errorf("failed to get status: %w", err)
	}

	var builder strings.Builder
	for _, file := range files {
		if rel, err := s.toRel(file, ctx.root); err == nil {
			if st, ok := status[rel]; ok {
				builder.WriteString(fmt.Sprintf("%c%c %s\n", formatStatusCode(st.Staging), formatStatusCode(st.Worktree), rel))
			}
		}
	}
	return builder.String(), nil
}

// GetChangedFiles returns a sorted list of all modified, added, or deleted files.
func (s *Service) GetChangedFiles() ([]string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return nil, err
	}

	status, err := ctx.worktree.Status()
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	var changed []string
	for path, st := range status {
		if st.Staging != git.Unmodified || st.Worktree != git.Unmodified {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// GetChangesForFiles generates a unified diff for the specified files against the HEAD commit.
func (s *Service) GetChangesForFiles(files []string) (string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}

	var headTree *object.Tree
	if ctx.head != nil {
		headTree, _ = ctx.head.Tree()
	}

	return s.generateBatchDiff(files, headTree, ctx.root)
}

// GetAmendChangesForFiles generates a diff comparing HEAD~1 to the current working tree.
func (s *Service) GetAmendChangesForFiles(files []string) (string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}

	var parentTree *object.Tree
	if ctx.head != nil && ctx.head.NumParents() > 0 {
		if parent, err := ctx.head.Parent(0); err == nil {
			parentTree, _ = parent.Tree()
		}
	}

	filesMap := make(map[string]bool)
	for _, f := range files {
		filesMap[f] = true
	}

	if ctx.head != nil {
		if headTree, err := ctx.head.Tree(); err == nil {
			_ = headTree.Files().ForEach(func(f *object.File) error {
				filesMap[f.Name] = true
				return nil
			})
		}
	}

	combined := make([]string, 0, len(filesMap))
	for f := range filesMap {
		combined = append(combined, f)
	}
	sort.Strings(combined)

	return s.generateBatchDiff(combined, parentTree, ctx.root)
}

// Commit stages the specified files and creates a new commit.
func (s *Service) Commit(files []string, message string) error {
	return s.performCommit(files, message, false)
}

// CommitAmend amends the last commit with the staged changes.
func (s *Service) CommitAmend(files []string, message string) error {
	return s.performCommit(files, message, true)
}

// Push pushes the current branch to the specified remote.
func (s *Service) Push(ctx context.Context, remoteName string) (string, error) {
	return s.performPush(ctx, remoteName, false)
}

// PushForce pushes the current branch with the force flag.
func (s *Service) PushForce(ctx context.Context, remoteName string) (string, error) {
	return s.performPush(ctx, remoteName, true)
}

// ResolvePath returns a list of all repository files within the given path.
func (s *Service) ResolvePath(path string) ([]string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(ctx.root, abs)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		if strings.HasPrefix(rel, "..") {
			return nil, ErrOutsideRepo
		}
		return []string{rel}, nil
	}

	status, _ := ctx.worktree.Status()
	headTree, _ := s.getHeadTree(ctx.repo)

	prefix := rel
	if prefix == "." {
		prefix = ""
	} else if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	seen := make(map[string]bool)
	var results []string
	add := func(p string) {
		if (prefix == "" || strings.HasPrefix(p, prefix)) && !seen[p] {
			results = append(results, p)
			seen[p] = true
		}
	}

	for p := range status {
		add(p)
	}
	if headTree != nil {
		_ = headTree.Files().ForEach(func(f *object.File) error {
			add(f.Name)
			return nil
		})
	}

	sort.Strings(results)
	return results, nil
}

// GetLastCommitMessage returns the message of the HEAD commit.
func (s *Service) GetLastCommitMessage() (string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}
	if ctx.head == nil {
		return "", errors.New("no commits found")
	}
	return ctx.head.Message, nil
}

// GetFilesInLastCommit returns a list of files changed in the HEAD commit.
func (s *Service) GetFilesInLastCommit() ([]string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return nil, err
	}
	if ctx.head == nil {
		return nil, nil
	}

	currentTree, _ := ctx.head.Tree()
	var parentTree *object.Tree
	if ctx.head.NumParents() > 0 {
		if parent, err := ctx.head.Parent(0); err == nil {
			parentTree, _ = parent.Tree()
		}
	}

	changes, err := object.DiffTree(parentTree, currentTree)
	if err != nil {
		return nil, fmt.Errorf("failed to diff tree: %w", err)
	}

	var files []string
	for _, change := range changes {
		name := change.To.Name
		if name == "" {
			name = change.From.Name
		}
		if name != "" {
			files = append(files, name)
		}
	}

	sort.Strings(files)
	return files, nil
}

// --- Internal Engine Logic ---

func (s *Service) getRepoContext() (*repoContext, error) {
	repo, wt, root, err := getRepo()
	if err != nil {
		return nil, err
	}
	ctx := &repoContext{repo: repo, worktree: wt, root: root}
	if head, err := repo.Head(); err == nil {
		ctx.head, _ = repo.CommitObject(head.Hash())
	}
	return ctx, nil
}

func (s *Service) toRel(path, root string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrOutsideRepo
	}
	return rel, nil
}

func (s *Service) generateBatchDiff(files []string, oldTree *object.Tree, root string) (string, error) {
	var builder strings.Builder
	for _, path := range files {
		rel, err := s.toRel(path, root)
		if err != nil {
			continue
		}
		diff, _ := s.diffFile(rel, path, oldTree)
		builder.WriteString(diff)
	}
	return builder.String(), nil
}

func (s *Service) diffFile(rel, fullPath string, oldTree *object.Tree) (string, error) {
	var oldText string
	isNew, isBinary := true, false

	if oldTree != nil {
		if f, err := oldTree.File(rel); err == nil {
			isBinary, _ = f.IsBinary()
			oldText, _ = f.Contents()
			isNew = false
		}
	}

	newBytes, err := os.ReadFile(filepath.Clean(fullPath))
	isDeleted := err != nil
	newText := string(newBytes)

	// Binary detection heuristic
	if !isBinary && !isDeleted {
		limit := BinaryScanLimit
		if len(newBytes) < limit {
			limit = len(newBytes)
		}
		for i := 0; i < limit; i++ {
			if newBytes[i] == 0 {
				isBinary = true
				break
			}
		}
	}

	if (isNew && isDeleted) || (oldText == newText && !isNew && !isDeleted) {
		return "", nil
	}

	if isBinary {
		return fmt.Sprintf("diff --git a/%s b/%s\nBinary files differ\n", rel, rel), nil
	}

	if len(oldText) > MaxDiffSize || len(newText) > MaxDiffSize {
		return fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\nBinary files or large files differ\n", rel, rel, rel, rel), nil
	}

	return generateDiffString(rel, oldText, newText, isNew, isDeleted), nil
}

func (s *Service) performCommit(files []string, message string, amend bool) error {
	ctx, err := s.getRepoContext()
	if err != nil {
		return err
	}

	for _, file := range files {
		rel, _ := s.toRel(file, ctx.root)
		if _, err := ctx.worktree.Add(rel); err != nil {
			return fmt.Errorf("failed to add %s: %w", rel, err)
		}
	}

	sig, err := s.getAuthorSignature(ctx.repo)
	if err != nil {
		return err
	}

	opts := &git.CommitOptions{Author: sig}
	if amend && ctx.head != nil {
		opts.Parents = ctx.head.ParentHashes
	}

	_, err = ctx.worktree.Commit(message, opts)
	return err
}

func (s *Service) performPush(ctx context.Context, remoteName string, force bool) (string, error) {
	rctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}

	localHead, _ := rctx.repo.Head()
	remote, err := rctx.repo.Remote(remoteName)
	if err != nil {
		return "", err
	}

	auth := resolveAuth(remote.Config().URLs[0])
	branchName := localHead.Name()
	refSpec := gitconfig.RefSpec(fmt.Sprintf("%s:%s", branchName, branchName))

	err = rctx.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: remoteName,
		Auth:       auth,
		RefSpecs:   []gitconfig.RefSpec{refSpec},
		Force:      force,
	})

	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("push failed: %w", err)
	}
	return "Push successful", nil
}

// --- Auth & Signature Helpers ---

func (s *Service) getAuthorSignature(r *git.Repository) (*object.Signature, error) {
	cfg, _ := r.Config()
	name, email := getNameEmail(cfg)

	if name == "" || email == "" {
		global, _ := gitconfig.LoadConfig(gitconfig.GlobalScope)
		gName, gEmail := getNameEmail(global)
		if name == "" {
			name = gName
		}
		if email == "" {
			email = gEmail
		}
	}

	if name == "" || email == "" {
		return nil, errors.New("git user config not found")
	}

	return &object.Signature{Name: name, Email: email, When: time.Now()}, nil
}

func getNameEmail(c *gitconfig.Config) (string, string) {
	if c == nil {
		return "", ""
	}
	return c.User.Name, c.User.Email
}

func resolveAuth(urlStr string) transport.AuthMethod {
	if strings.HasPrefix(urlStr, "http") {
		return nil
	}

	user := "git"
	if parts := strings.Split(urlStr, "@"); len(parts) > 1 {
		user = strings.TrimPrefix(parts[0], "ssh://")
	}

	auth := &gitssh.PublicKeysCallback{
		User: user,
		Callback: func() ([]ssh.Signer, error) {
			var signers []ssh.Signer
			if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
				if conn, err := (&net.Dialer{}).Dial("unix", sock); err == nil {
					s, _ := agent.NewClient(conn).Signers()
					signers = append(signers, s...)
				}
			}
			if len(signers) > 0 {
				return signers, nil
			}

			home, _ := os.UserHomeDir()
			for _, n := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
				if key, err := os.ReadFile(filepath.Join(home, ".ssh", n)); err == nil {
					if s, err := ssh.ParsePrivateKey(key); err == nil {
						signers = append(signers, s)
					}
				}
			}
			return signers, nil
		},
	}
	auth.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	return auth
}

// ExtractVersionFromDiff scans a unified diff for lines that look like version updates.
func ExtractVersionFromDiff(diffText string) string {
	lines := strings.Split(diffText, "\n")
	versionRegex := regexp.MustCompile(`([0-9]+\.[0-9][0-9a-z.-]*)`)

	var oldVersion, newVersion string
	var currentFile string

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			if parts := strings.Fields(line); len(parts) >= 4 {
				currentFile = filepath.Base(parts[3])
			}
			continue
		}

		if !isVersionFile(currentFile) {
			continue
		}

		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "version") && !strings.Contains(lowerLine, "versioning") {
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				if matches := versionRegex.FindStringSubmatch(line[1:]); len(matches) > 1 {
					oldVersion = matches[1]
				}
			}
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				if matches := versionRegex.FindStringSubmatch(line[1:]); len(matches) > 1 {
					newVersion = matches[1]
				}
			}
		}

		if oldVersion != "" && newVersion != "" && oldVersion != newVersion {
			return fmt.Sprintf("%s -> %s", oldVersion, newVersion)
		}
	}
	return newVersion
}

// --- Utility Helpers ---

func isVersionFile(filename string) bool {
	f := strings.ToLower(filename)
	if strings.Contains(f, "test") || strings.Contains(f, "_spec") {
		return false
	}
	targets := []string{
		"version", "package.json", "go.mod", "cargo.toml", "pyproject.toml",
		"composer.json", "gemfile", "mix.exs", "version.rb", "version.py",
		"setup.py", "cmakelists.txt",
	}
	for _, t := range targets {
		if strings.EqualFold(f, t) {
			return true
		}
	}
	return false
}

func getRepo() (*git.Repository, *git.Worktree, string, error) {
	root, err := GetGitRoot()
	if err != nil {
		return nil, nil, "", err
	}
	repo, err := git.PlainOpen(root)
	if err != nil {
		return nil, nil, "", err
	}
	wt, err := repo.Worktree()
	return repo, wt, root, err
}

func (s *Service) getHeadTree(repo *git.Repository) (*object.Tree, error) {
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}
	return commit.Tree()
}

func formatStatusCode(c git.StatusCode) rune {
	mapping := map[git.StatusCode]rune{
		git.Unmodified: ' ', git.Modified: 'M', git.Added: 'A',
		git.Deleted: 'D', git.Renamed: 'R', git.Copied: 'C', git.Untracked: '?',
	}
	if r, ok := mapping[c]; ok {
		return r
	}
	return '?'
}

// generateDiffString creates a unified diff compatible with standard git output.
//
// NOTE: We intentionally use the verbose "diff --git" header format (instead of
// simpler custom formats like "M file.go") because LLMs perform significantly
// better with it. The standard git headers act as strong "mental anchors" that
// prevent context bleeding between files and align with the model's training data.
func generateDiffString(path, oldText, newText string, isNew, isDel bool) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("diff --git a/%s b/%s\n(Diff failed: %v)\n", path, path, r)
		}
	}()

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldText, newText, false)
	dmp.DiffCleanupSemantic(diffs)

	patches := dmp.PatchMake(oldText, newText)
	decoded, _ := url.PathUnescape(dmp.PatchToText(patches))

	var b strings.Builder
	b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
	if isNew {
		b.WriteString(fmt.Sprintf("new file mode 100644\n--- /dev/null\n+++ b/%s\n", path))
	} else if isDel {
		b.WriteString(fmt.Sprintf("deleted file mode 100644\n--- a/%s\n+++ /dev/null\n", path))
	} else {
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path))
	}
	b.WriteString(decoded)
	return b.String()
}

func (s *Service) GetPullRequestURL(remoteName string) (string, error) {
	ctx, err := s.getRepoContext()
	if err != nil {
		return "", err
	}

	head, err := ctx.repo.Head()
	if err != nil {
		return "", err
	}
	branch := head.Name().Short()

	remote, err := ctx.repo.Remote(remoteName)
	if err != nil || len(remote.Config().URLs) == 0 {
		return "", err
	}

	urlStr := normalizeGitURL(remote.Config().URLs[0])
	switch {
	case strings.Contains(urlStr, "github.com"):
		return fmt.Sprintf("%s/pull/new/%s", urlStr, branch), nil
	case strings.Contains(urlStr, "gitlab.com"):
		return fmt.Sprintf("%s/-/merge_requests/new?merge_request[source_branch]=%s", urlStr, branch), nil
	case strings.Contains(urlStr, "bitbucket.org"):
		return fmt.Sprintf("%s/pull-requests/new?source=%s", urlStr, branch), nil
	}
	return "", fmt.Errorf("unsupported host: %s", urlStr)
}

func normalizeGitURL(rawURL string) string {
	u := strings.TrimSuffix(strings.TrimSpace(rawURL), ".git")
	if strings.HasPrefix(u, "git@") {
		u = "https://" + strings.Replace(strings.TrimPrefix(u, "git@"), ":", "/", 1)
	} else if strings.HasPrefix(u, "ssh://") {
		parts := strings.Split(u, "@")
		if len(parts) > 1 {
			u = "https://" + parts[1]
		}
	}
	if !strings.HasPrefix(u, "http") {
		u = "https://" + u
	}
	return u
}
