_kmonad_device_manager() {
  local current previous
  current="${COMP_WORDS[COMP_CWORD]}"
  previous="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$previous" in
    --completion)
      COMPREPLY=( $(compgen -W 'bash zsh fish' -- "$current") )
      return
      ;;
  esac

  COMPREPLY=( $(compgen -W '--doctor --help -h --completion --version --status --status=json --json ps devices identify validate apply config' -- "$current") )
}

complete -F _kmonad_device_manager kmonad-device-manager
