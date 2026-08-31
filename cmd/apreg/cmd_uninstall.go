package main

import (
	"fmt"
	"os"
	"sort"
)

func cmdList(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: apreg list")
	}
	lf, err := loadLockfile()
	if err != nil {
		return fmt.Errorf("read %s: %w", lockfileName, err)
	}
	if len(lf.Packages) == 0 {
		fmt.Println("No plugins installed in this directory.")
		return nil
	}

	refs := make([]string, 0, len(lf.Packages))
	for ref := range lf.Packages {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		e := lf.Packages[ref]
		fmt.Printf("%s@%s — %s\n", ref, e.Version, e.Dir)
	}
	return nil
}

func cmdUninstall(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: apreg uninstall @scope/name")
	}
	ref := args[0]
	if _, _, _, err := parseRef(ref); err != nil {
		return err
	}

	lf, err := loadLockfile()
	if err != nil {
		return fmt.Errorf("read %s: %w", lockfileName, err)
	}
	e, ok := lf.Packages[ref]
	if !ok {
		return fmt.Errorf("%s is not tracked as installed in this directory (see %s)", ref, lockfileName)
	}

	if err := os.RemoveAll(e.Dir); err != nil {
		return fmt.Errorf("remove %s: %w", e.Dir, err)
	}
	delete(lf.Packages, ref)
	if err := saveLockfile(lf); err != nil {
		return fmt.Errorf("update %s: %w", lockfileName, err)
	}

	fmt.Printf("Uninstalled %s (removed %s)\n", ref, e.Dir)
	return nil
}
