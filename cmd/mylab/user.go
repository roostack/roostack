package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/store"
)

const userUsage = `Usage:
  mylab user add <name> [-role owner|admin|member]
  mylab user reset-password <name>

Both print a new temporary password. Run as root or the mylab user.
`

// user manages accounts from the host command line. Password reset is
// deliberately CLI-only (SPEC.md §5).
func user(args []string) error {
	if len(args) < 2 {
		fmt.Print(userUsage)
		return errors.New("user: missing arguments")
	}
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	role := fs.String("role", auth.RoleMember, "role for a new user")
	sub, name := args[0], args[1]
	fs.Parse(args[2:])

	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath(lab))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	password, err := auth.NewToken()
	if err != nil {
		return err
	}
	password = password[:20] // still ~120 bits of randomness
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	switch sub {
	case "add":
		if !auth.ValidRole(*role) {
			return fmt.Errorf("user: unknown role %q", *role)
		}
		if _, err := st.CreateUser(ctx, store.User{Username: name, PasswordHash: hash, Role: *role, MustChangePassword: true}); err != nil {
			return err
		}
		st.Audit(ctx, nil, "user add", fmt.Sprintf("%s (%s) via command line", name, *role))
		fmt.Printf("Created %s (%s).\n", name, *role)
	case "reset-password":
		if err := st.SetPassword(ctx, name, hash, true); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("user: no user named %q", name)
		} else if err != nil {
			return err
		}
		st.Audit(ctx, nil, "password reset", name+" via command line")
		fmt.Printf("Password reset for %s. They have been logged out everywhere.\n", name)
	default:
		fmt.Print(userUsage)
		return fmt.Errorf("user: unknown command %q", sub)
	}
	fmt.Printf("Temporary password: %s\n", password)
	fmt.Println("They choose their own password when they next log in.")
	return nil
}
