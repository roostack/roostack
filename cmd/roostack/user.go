package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/store"
)

const userUsage = `Usage:
  roostack user add <name> [-role owner|admin|member]
  roostack user reset-password <name>
  roostack user reset-2fa <name>       turn off two-factor (a lost phone)

add and reset-password print a temporary password, to be changed at the
next login. Run as root or the roostack user: started as root, it will run
as the roostack user.
`

// user manages accounts from the host command line: the way back in for
// an Owner who forgot their password or lost their phone (SPEC.md §5).
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
	st, err := store.Open(lab.Paths.DatabaseFile())
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	password, err := auth.NewTemporaryPassword()
	if err != nil {
		return err
	}
	hash, err := authkit.HashPassword(password)
	if err != nil {
		return err
	}

	switch sub {
	case "reset-2fa":
		u, err := st.UserByUsername(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("user: no user named %q", name)
		} else if err != nil {
			return err
		}
		if err := st.ResetTOTP(ctx, u.ID); err != nil {
			return err
		}
		st.Audit(ctx, nil, "2FA reset", name+" via command line")
		fmt.Printf("Two-factor authentication is off for %s, and they have been logged out everywhere.\n", name)
		if u.Role == auth.RoleOwner {
			fmt.Println("As an Owner, they'll set it up again when they next log in.")
		}
		return nil
	case "add":
		if !auth.ValidUsername(name) {
			return fmt.Errorf("user: %q isn't a valid username (letters, digits, . _ -; up to 32)", name)
		}
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
	fmt.Println("They'll choose their own password when they next log in.")
	return nil
}
