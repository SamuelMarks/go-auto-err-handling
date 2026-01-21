// Package files provides tests for the file collection utility. 
package files

import ( 
  "os" 
  "path/filepath" 
  "reflect" 
  "sort" 
  "testing" 
) 

// TestCollectGoFiles tests the CollectGoFiles function with various scenarios. 
// It covers normal collection, exclusions, error cases, and ensures 100% coverage of branches. 
// 
// t: the testing context. 
func TestCollectGoFiles(t *testing.T) { 
  tests := []struct { 
    // name is the name of the test case. 
    name string
    // setup is a function to set up the temporary directory. 
    setup func(dir string) error
    // dir is the directory to pass to CollectGoFiles (will be overridden to tempdir). 
    dir string
    // excludeGlobs are the globs to exclude. 
    excludeGlobs []string
    // want is the expected list of base file names (sorted). 
    want []string
    // wantErr indicates if an error is expected. 
    wantErr bool
  }{ 
    { 
      name: "no files", 
      setup: func(dir string) error { 
        return nil
      }, 
      dir:  "", 
      want: nil, 
    }, 
    { 
      name: "collect basic go files", 
      setup: func(dir string) error { 
        if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "notgo.txt"), []byte("text"), 0644); err != nil { 
          return err
        } 
        return nil
      }, 
      dir:  "", 
      want: []string{"main.go", "other.go"}, 
    }, 
    { 
      name: "exclude specific file", 
      setup: func(dir string) error { 
        if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        return nil
      }, 
      excludeGlobs: []string{"main.go"}, 
      want:         []string{"other.go"}, 
    }, 
    { 
      name: "exclude directory", 
      setup: func(dir string) error { 
        if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.Mkdir(filepath.Join(dir, "vendor"), 0755); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "vendor", "lib.go"), []byte("package lib"), 0644); err != nil { 
          return err
        } 
        return nil
      }, 
      excludeGlobs: []string{"vendor"}, 
      want:         []string{"main.go"}, 
    }, 
    { 
      name: "exclude directory explicitly skips contents", 
      setup: func(dir string) error { 
        if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.Mkdir(filepath.Join(dir, "vendor"), 0755); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "vendor", "lib.go"), []byte("package lib"), 0644); err != nil { 
          return err
        } 
        return nil 
      }, 
      // Note: filepath.Match("vendor/*", "vendor/lib.go") is false on Unix. 
      // To exclude recursively with filepath.Match, one typically excludes the directory itself. 
      excludeGlobs: []string{"vendor"}, 
      want:         []string{"main.go"}, 
    }, 
    { 
      name: "multiple excludes", 
      setup: func(dir string) error { 
        if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main"), 0644); err != nil { 
          return err
        } 
        if err := os.Mkdir(filepath.Join(dir, "exclude"), 0755); err != nil { 
          return err
        } 
        if err := os.WriteFile(filepath.Join(dir, "exclude", "excluded.go"), []byte("package ex"), 0644); err != nil { 
          return err
        } 
        return nil
      }, 
      excludeGlobs: []string{"other.go", "exclude"}, 
      want:         []string{"main.go"}, 
    }, 
    { 
      name: "invalid directory", 
      setup: func(dir string) error { 
        return nil
      }, 
      dir:     "/nonexistent/directory", 
      wantErr: true, 
    }, 
    { 
      name: "permission denied", 
      setup: func(dir string) error { 
        if err := os.Mkdir(filepath.Join(dir, "noperm"), 0000); err != nil { 
          return err
        } 
        return nil
      }, 
      dir:     "", 
      wantErr: true, 
    }, 
  } 

  for _, tt := range tests { 
    t.Run(tt.name, func(t *testing.T) { 
      tempDir, err := os.MkdirTemp("", "collect-test-*") 
      if err != nil { 
        t.Fatal(err) 
      } 
      defer os.RemoveAll(tempDir) 

      dir := tempDir
      if tt.dir != "" { 
        dir = tt.dir
      } else if tt.setup != nil { 
        if err := tt.setup(tempDir); err != nil { 
          t.Fatal(err) 
        } 
      } 

      got, err := CollectGoFiles(dir, tt.excludeGlobs) 
      if (err != nil) != tt.wantErr { 
        t.Errorf("CollectGoFiles() error = %v, wantErr %v", err, tt.wantErr) 
        return
      } 
      if tt.wantErr { 
        return
      } 

      var gotBases []string
      for _, p := range got { 
        gotBases = append(gotBases, filepath.Base(p)) 
      } 
      sort.Strings(gotBases) 
      sort.Strings(tt.want) 

      if !reflect.DeepEqual(gotBases, tt.want) { 
        t.Errorf("CollectGoFiles() got = %v, want %v", gotBases, tt.want) 
      } 
    }) 
  } 
}