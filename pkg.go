package depth

import (
	"bytes"
	"fmt"
	"go/build"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pkg represents a Go source package, and its dependencies.
type Pkg struct {
	Name   string `json:"name"`
	SrcDir string `json:"-"`

	Internal bool `json:"internal"`
	Resolved bool `json:"resolved"`
	Test     bool `json:"-"`

	Tree   *Tree `json:"-"`
	Parent *Pkg  `json:"-"`

	lockDeps sync.Mutex
	Deps     []Pkg `json:"deps"`

	Raw *build.Package `json:"-"`

	// importLock sync.Mutex
}

type buildTask struct {
	// flag bool
	pkg *Pkg
	// get chan bool
	// put chan *Pkg
	wait sync.WaitGroup
}

var g_lockCache sync.Mutex
var g_cache = map[string]*buildTask{}

var OnlyParse string

func getCache(name string, pkg *Pkg, i Importer) *Pkg {
	g_lockCache.Lock()

	if elem, ok := g_cache[name]; ok {
		if elem.pkg != nil {
			g_lockCache.Unlock()
			return elem.pkg
		} else {
			g_lockCache.Unlock()
			elem.wait.Wait()
			if elem.pkg == nil {
				panic(111)
			}
			return elem.pkg
		}
	} else {
		task := &buildTask{
			// get: make(chan bool),
		}

		g_cache[name] = task
		g_lockCache.Unlock()
		task.wait.Add(1)
		defer func() {
			task.wait.Done()
		}()
		pkg.resolveImpl(i)
		task.pkg = pkg

		return task.pkg
	}
}

func (p *Pkg) resolveImpl(i Importer) {

	p.Resolved = true

	name := p.cleanName()
	if name == "" {
		return
	}

	// Stop resolving imports if we've reached max depth or found a duplicate.
	var importMode build.ImportMode
	if p.Tree.hasSeenImport(name) || p.Tree.isAtMaxDepth(p) {
		importMode = build.FindOnly
	}

	var pkg *build.Package
	var err error

	begin := time.Now().Unix()
	pkg, err = i.Import(name, p.SrcDir, importMode)
	end := time.Now().Unix()
	if (end - begin) >= 2 {
		fmt.Println("解析包超过2秒", end-begin, name)
	}

	// fmt.Println(d.Milliseconds())
	fmt.Println("goroot解析导入包 -> ", time.Now(), p.Name, name, p.SrcDir)

	if err != nil {
		// TODO: Check the error type?
		p.Resolved = false
		return
	}

	var usageImports []string
	for _, v := range pkg.Imports {
		if strings.HasPrefix(v, "git.dustess.com") {
			usageImports = append(usageImports, v)
		}
	}

	pkg.Imports = usageImports

	p.Raw = pkg

	// Update the name with the fully qualified import path.
	p.Name = pkg.ImportPath

	// If this is an internal dependency, we may need to skip it.
	if pkg.Goroot {
		p.Internal = true
		if !p.Tree.shouldResolveInternal(p) {
			return
		}
	}

	//first we set the regular dependencies, then we add the test dependencies
	//sharing the same set. This allows us to mark all test-only deps linearly
	// unique := make(map[string]struct{})
	p.setDeps(i, pkg.Imports, pkg.Dir /*unique*/, false)
	if p.Tree.ResolveTest {
		p.setDeps(i, append(pkg.TestImports, pkg.XTestImports...), pkg.Dir, true)
	}

	// g_lockCache.Lock()
	// defer g_lockCache.Unlock()

	// g_cache[name] = p
}

// Resolve recursively finds all dependencies for the Pkg and the packages it depends on.
func (p *Pkg) Resolve(i Importer) {
	name := p.cleanName()
	if alreadyParsed := getCache(name, p, i); alreadyParsed != nil {
		*p = *alreadyParsed
		return
		// pkg = alreadyParsed
	}

	return

	// Resolved is always true, regardless of if we skip the import,
	// it is only false if there is an error while importing.
	p.Resolved = true

	name = p.cleanName()
	if name == "" {
		return
	}

	// Stop resolving imports if we've reached max depth or found a duplicate.
	var importMode build.ImportMode
	if p.Tree.hasSeenImport(name) || p.Tree.isAtMaxDepth(p) {
		importMode = build.FindOnly
	}

	var pkg *build.Package
	var err error
	if alreadyParsed := getCache(name, p, i); alreadyParsed != nil {
		fmt.Println(name, "已经分析过，跳过......")
		*p = *alreadyParsed
		return
		// pkg = alreadyParsed
	} else {
		begin := time.Now().Unix()
		pkg, err = i.Import(name, p.SrcDir, importMode)
		end := time.Now().Unix()
		if (end - begin) >= 2 {
			fmt.Println("解析包超过2秒", end-begin, name)
		}

		// fmt.Println(d.Milliseconds())
		fmt.Println("goroot解析导入包 -> ", time.Now(), p.Name, name, p.SrcDir)
	}

	if err != nil {
		// TODO: Check the error type?
		p.Resolved = false
		return
	}

	var usageImports []string
	for _, v := range pkg.Imports {
		if strings.HasPrefix(v, "git.dustess.com") {
			usageImports = append(usageImports, v)
		}
	}

	pkg.Imports = usageImports

	p.Raw = pkg

	// Update the name with the fully qualified import path.
	p.Name = pkg.ImportPath

	// If this is an internal dependency, we may need to skip it.
	if pkg.Goroot {
		p.Internal = true
		if !p.Tree.shouldResolveInternal(p) {
			return
		}
	}

	//first we set the regular dependencies, then we add the test dependencies
	//sharing the same set. This allows us to mark all test-only deps linearly
	// unique := make(map[string]struct{})
	p.setDeps(i, pkg.Imports, pkg.Dir /*unique*/, false)
	if p.Tree.ResolveTest {
		p.setDeps(i, append(pkg.TestImports, pkg.XTestImports...), pkg.Dir, true)
	}

	// g_lockCache.Lock()
	// defer g_lockCache.Unlock()

	// g_cache[name] = p
}

// setDeps takes a slice of import paths and the source directory they are relative to,
// and creates the Deps of the Pkg. Each dependency is also further resolved prior to being added
// to the Pkg.
func (p *Pkg) setDeps(i Importer, imports []string, srcDir string /*unique map[string]struct{}, */, isTest bool) {
	var wait sync.WaitGroup
	fn := func(imp string) {
		defer wait.Done()

		if imp == p.Name {
			return
		}

		// Skip duplicates.
		// if _, ok := unique[imp]; ok {
		// 	return
		// }
		// unique[imp] = struct{}{}

		p.addDep(i, imp, srcDir, isTest)
	}

	for _, imp := range imports {
		wait.Add(1)
		go fn(imp)
		// // Mostly for testing files where cyclic imports are allowed.
		// if imp == p.Name {
		// 	continue
		// }

		// // Skip duplicates.
		// if _, ok := unique[imp]; ok {
		// 	continue
		// }
		// unique[imp] = struct{}{}

		// p.addDep(i, imp, srcDir, isTest)
	}

	wait.Wait()

	p.lockDeps.Lock()
	defer p.lockDeps.Unlock()

	sort.Sort(byInternalAndName(p.Deps))
}

// addDep creates a Pkg and it's dependencies from an imported package name.
func (p *Pkg) addDep(i Importer, name string, srcDir string, isTest bool) {

	dep := Pkg{
		Name:   name,
		SrcDir: srcDir,
		Tree:   p.Tree,
		Parent: p,
		Test:   isTest,
	}
	dep.Resolve(i)

	p.lockDeps.Lock()
	defer p.lockDeps.Unlock()

	p.Deps = append(p.Deps, dep)

	// dep.Resolve(i)
}

// isParent goes recursively up the chain of Pkgs to determine if the name provided is ever a
// parent of the current Pkg.
func (p *Pkg) isParent(name string) bool {
	if p.Parent == nil {
		return false
	}

	if p.Parent.Name == name {
		return true
	}

	return p.Parent.isParent(name)
}

// depth returns the depth of the Pkg within the Tree.
func (p *Pkg) depth() int {
	if p.Parent == nil {
		return 0
	}

	return p.Parent.depth() + 1
}

// cleanName returns a cleaned version of the Pkg name used for resolving dependencies.
//
// If an empty string is returned, dependencies should not be resolved.
func (p *Pkg) cleanName() string {
	name := p.Name

	// C 'package' cannot be resolved.
	if name == "C" {
		return ""
	}

	// Internal golang_org/* packages must be prefixed with vendor/
	//
	// Thanks to @davecheney for this:
	// https://github.com/davecheney/graphpkg/blob/master/main.go#L46
	if strings.HasPrefix(name, "golang_org") {
		name = path.Join("vendor", name)
	}

	return name
}

// String returns a string representation of the Pkg containing the Pkg name and status.
func (p *Pkg) String() string {
	b := bytes.NewBufferString(p.Name)

	if !p.Resolved {
		b.Write([]byte(" (unresolved)"))
	}

	return b.String()
}

// byInternalAndName ensures a slice of Pkgs are sorted such that the internal stdlib
// packages are always above external packages (ie. github.com/whatever).
type byInternalAndName []Pkg

func (b byInternalAndName) Len() int {
	return len(b)
}

func (b byInternalAndName) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}

func (b byInternalAndName) Less(i, j int) bool {
	if b[i].Internal && !b[j].Internal {
		return true
	} else if !b[i].Internal && b[j].Internal {
		return false
	}

	return b[i].Name < b[j].Name
}
