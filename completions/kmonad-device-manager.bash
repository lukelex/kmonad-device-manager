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

  if [[ "${COMP_WORDS[1]}" == "config" ]]; then
    COMPREPLY=( $(compgen -W 'list read export create update enable disable delete adopt --idempotency-key' -- "$current") )
    return
  fi

  if [[ "${COMP_WORDS[1]}" == "inputscan" ]]; then
    COMPREPLY=( $(compgen -W 'get probe status cancel' -- "$current") )
    return
  fi

  COMPREPLY=( $(compgen -W '--doctor --help -h --completion --version --status --json --idempotency-key ps devices manager snapshot events identify inputscan validate apply config' -- "$current") )
}

complete -F _kmonad_device_manager kmonad-device-manager
