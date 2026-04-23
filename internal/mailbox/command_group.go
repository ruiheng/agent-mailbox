package mailbox

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

func (a *App) prepareGroupCommand(args []string) (preparedCommand, error) {
	if len(args) == 0 {
		return nil, errors.New("expected a group subcommand: create, add-member, remove-member, or members")
	}
	if isHelpArg(args[0]) {
		a.writeGroupHelp()
		return nil, ErrHelpRequested
	}

	switch args[0] {
	case "create":
		return a.prepareGroupCreateCommand(args[1:])
	case "add-member":
		return a.prepareGroupAddMemberCommand(args[1:])
	case "remove-member":
		return a.prepareGroupRemoveMemberCommand(args[1:])
	case "members":
		return a.prepareGroupMembersCommand(args[1:])
	default:
		return nil, fmt.Errorf("unknown group subcommand %q", args[0])
	}
}

func (a *App) prepareGroupCreateCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupCreateHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	groupAddress, err := NormalizeAddress(groupAddress)
	if err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		group, err := store.CreateGroup(ctx, groupAddress)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, group)
		}
		_, err = fmt.Fprintf(a.stdout, "group_id=%s address=%s\n", group.GroupID, group.Address)
		return err
	}, nil
}

func (a *App) prepareGroupAddMemberCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group add-member", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var person string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	fs.StringVar(&person, "person", "", "person identity")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupAddMemberHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	groupAddress, err := NormalizeAddress(groupAddress)
	if err != nil {
		return nil, err
	}
	if err := requireFlag(person, "--person"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		membership, err := store.AddGroupMember(ctx, groupAddress, person)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, membership)
		}
		_, err = fmt.Fprintf(
			a.stdout,
			"membership_id=%s group=%s person=%s active=%t joined_at=%s\n",
			membership.MembershipID,
			membership.GroupAddress,
			membership.Person,
			membership.Active,
			membership.JoinedAt,
		)
		return err
	}, nil
}

func (a *App) prepareGroupRemoveMemberCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group remove-member", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var person string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	fs.StringVar(&person, "person", "", "person identity")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupRemoveMemberHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	groupAddress, err := NormalizeAddress(groupAddress)
	if err != nil {
		return nil, err
	}
	if err := requireFlag(person, "--person"); err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		membership, err := store.RemoveGroupMember(ctx, groupAddress, person)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, membership)
		}
		leftAt := ""
		if membership.LeftAt != nil {
			leftAt = *membership.LeftAt
		}
		_, err = fmt.Fprintf(
			a.stdout,
			"membership_id=%s group=%s person=%s active=%t joined_at=%s left_at=%s\n",
			membership.MembershipID,
			membership.GroupAddress,
			membership.Person,
			membership.Active,
			membership.JoinedAt,
			leftAt,
		)
		return err
	}, nil
}

func (a *App) prepareGroupMembersCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox group members", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var groupAddress string
	var formats outputFlags
	fs.StringVar(&groupAddress, "group", "", "group address")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeGroupMembersHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(groupAddress, "--group"); err != nil {
		return nil, err
	}
	groupAddress, err := NormalizeAddress(groupAddress)
	if err != nil {
		return nil, err
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, store *Store) error {
		memberships, err := store.ListGroupMembers(ctx, groupAddress)
		if err != nil {
			return err
		}
		if format != outputFormatText {
			return a.writeStructuredOutput(format, memberships)
		}
		for _, membership := range memberships {
			leftAt := ""
			if membership.LeftAt != nil {
				leftAt = *membership.LeftAt
			}
			if _, err := fmt.Fprintf(
				a.stdout,
				"membership_id=%s person=%s active=%t joined_at=%s left_at=%s\n",
				membership.MembershipID,
				membership.Person,
				membership.Active,
				membership.JoinedAt,
				leftAt,
			); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func (a *App) writeGroupHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group <subcommand> [options]",
		"",
		"Subcommands:",
		"  create              Create a group address",
		"  add-member          Add a person to a group",
		"  remove-member       Remove a person from a group",
		"  members             List current and historical memberships",
	})
}

func (a *App) writeGroupCreateHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group create --group ADDRESS [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupAddMemberHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group add-member --group ADDRESS --person PERSON [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --person PERSON     Person identity",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupRemoveMemberHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group remove-member --group ADDRESS --person PERSON [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --person PERSON     Person identity",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}

func (a *App) writeGroupMembersHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox group members --group ADDRESS [--json | --yaml]",
		"",
		"Options:",
		"  --group ADDRESS     Group address",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
	})
}
