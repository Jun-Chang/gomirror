package main

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var List = []string{}

func main() {
	owner := os.Getenv("GOMIRROR_OWNER")
	repo := os.Getenv("GOMIRROR_REPO")
	token := os.Getenv("GOMIRROR_TOKEN")
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(oauth2.NoContext, ts)

	cl := github.NewClient(tc)

	//push(owner, repo, cl)
	pull(owner, repo, cl)
}

func pull(owner string, repo string, cl *github.Client) {
	srv := cl.Git

	// master refを取得
	mRef, _, err := srv.GetRef(owner, repo, "heads/master")
	if err != nil {
		panic(fmt.Errorf("GerRef master %v", err))
	}

	base := os.TempDir() + "gomirror"
	if err := os.Mkdir(base, 0755); err != nil {
		panic(fmt.Errorf("%s create %v", base, err))
	}

	// tree refを取得
	tree, _, err := srv.GetTree(owner, repo, *mRef.Object.SHA, true)
	if err != nil {
		panic(fmt.Errorf("GetTree %v", err))
	}

	total := len(tree.Entries)

	done := make(chan bool, total)
	pool := make(chan bool, 100)
	for _, e := range tree.Entries {
		path := base + "/" + *e.Path
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err != nil {
			if err := os.Mkdir(dir, 0755); err != nil {
				panic(fmt.Errorf("%s create %v", dir, err))
			}
		}
		go func(e github.TreeEntry) {
			pool <- true

			defer func() {
				done <- true
			}()

			if *e.Type != "blob" {
				return
			}

			// blobを取得
			blob, _, err := srv.GetBlob(owner, repo, *e.SHA)
			if err != nil {
				panic(fmt.Errorf("blob %s %v", path, err))
			}
			// decode
			dec, err := base64.StdEncoding.DecodeString(*blob.Content)
			if err != nil {
				panic(fmt.Errorf("base64 decode %s %v", path, err))
			}
			// write file
			if err := ioutil.WriteFile(path, dec, 0644); err != nil {
				panic(fmt.Errorf("writeFile %s %v", path, err))
			}
		}(e)
	}

	i := 0
	for {
		<-done
		i++
		<-pool
		if i >= total {
			break
		}
	}

	if err := os.RemoveAll("./vendor"); err != nil {
		panic(fmt.Errorf("remove vendor %v", err))
	}
	if err := os.Rename(base, "./vendor"); err != nil {
		panic(fmt.Errorf("rename vendor %v", err))
	}

	fmt.Println("pull done")
}

func push(owner string, repo string, cl *github.Client) {
	srv := cl.Git

	// master refを取得
	mRef, _, err := srv.GetRef(owner, repo, "heads/master")
	if err != nil {
		panic(fmt.Errorf("GerRef master %v", err))
	}

	// branchを作成
	branchName := "feature/" + strconv.Itoa(int(time.Now().Unix()))
	ref := &github.Reference{
		Ref: github.String("refs/heads/" + branchName),
		Object: &github.GitObject{
			SHA: mRef.Object.SHA,
		},
	}
	if _, _, err := srv.CreateRef(owner, repo, ref); err != nil {
		panic(fmt.Errorf("Create Ref %v", err))
	}

	// list作成
	vendor := "vendor"
	// vendor directory not valid.
	if _, err := os.Stat(vendor); err != nil {
		panic("vendor directory is not exists")
	}

	filepath.Walk(vendor, visit)

	// blob作成
	entries := make([]github.TreeEntry, 0, len(List))
	for _, l := range List {
		path := strings.Replace(l, "vendor/", "", 1)
		fmt.Println(path)

		b, err := ioutil.ReadFile(l)
		if err != nil {
			panic(fmt.Errorf("ReadFile %s %v", l, err))
		}
		str := string(b)
		blb := &github.Blob{
			Content:  github.String(str),
			Encoding: github.String("utf-8"),
			Size:     github.Int(len(str)),
		}

		var resBlb *github.Blob
		retry := 10
		for i := 0; i <= retry; i++ {
			resBlb, _, err = srv.CreateBlob(owner, repo, blb)
			if err == nil {
				break
			}
			if i == retry {
				panic(fmt.Errorf("CreateBlob %s retry exeed.", l))
			}
			fmt.Println("failed. sleep a few times..")
			time.Sleep(5000 * time.Millisecond)
		}
		entries = append(entries, github.TreeEntry{
			SHA:  resBlb.SHA,
			Path: github.String(path),
			Mode: github.String("100644"),
			Type: github.String("blob"),
		})
	}

	// tree作成
	//tree, _, err := srv.CreateTree(owner, repo, *ref.Object.SHA, []github.TreeEntry{
	tree, _, err := srv.CreateTree(owner, repo, "", entries)
	if err != nil {
		panic(fmt.Errorf("CreateTree %v", err))
	}

	// commit
	parent, _, err := srv.GetCommit(owner, repo, *ref.Object.SHA)
	if err != nil {
		panic(err)
	}
	commit := &github.Commit{
		Message: github.String("commit from golang!"),
		Tree:    tree,
		Parents: []github.Commit{*parent},
	}
	resCommit, _, err := srv.CreateCommit(owner, repo, commit)
	if err != nil {
		panic(err)
	}

	// update branch ref
	nref := &github.Reference{
		Ref: github.String("refs/heads/" + branchName),
		Object: &github.GitObject{
			Type: github.String("commit"),
			SHA:  resCommit.SHA,
		},
	}
	if _, _, err := srv.UpdateRef(owner, repo, nref, false); err != nil {
		panic(err)
	}

	// create pull request
	prSrv := cl.PullRequests
	npr := &github.NewPullRequest{
		Title: github.String("test"),
		Head:  github.String(owner + ":" + branchName),
		Base:  github.String("master"),
		Body:  github.String("test"),
	}
	if _, _, err := prSrv.Create(owner, repo, npr); err != nil {
		panic(fmt.Errorf("Pull Request Create %v", err))
	}

	fmt.Println("push done")
}

func visit(path string, f os.FileInfo, err error) error {
	if f.IsDir() {
		return nil
	}
	if strings.Contains(path, ".git") {
		return nil
	}
	if strings.Contains(path, "_test.go") {
		return nil
	}
	if filepath.Ext(path) != ".go" {
		return nil
	}
	List = append(List, path)

	return nil
}
