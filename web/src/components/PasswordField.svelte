<script lang="ts">
  import Icon from './Icon.svelte';
  import { t } from '../lib/i18n/index.svelte';

  let {
    value = $bindable(''),
    placeholder,
    autofocus = false,
    invalid = false,
    id,
    autocomplete = 'new-password',
    onenter,
  }: {
    value?: string;
    placeholder: string;
    autofocus?: boolean;
    invalid?: boolean;
    id?: string;
    autocomplete?: 'new-password' | 'current-password' | 'off';
    onenter?: () => void;
  } = $props();

  let shown = $state(false);
  let input: HTMLInputElement;

  $effect(() => {
    if (autofocus) input?.focus();
  });
</script>

<div class="pw" class:invalid>
  <input
    bind:this={input}
    {id}
    class="input"
    type={shown ? 'text' : 'password'}
    bind:value
    {placeholder}
    aria-label={placeholder}
    aria-invalid={invalid}
    {autocomplete}
    spellcheck="false"
    autocapitalize="off"
    onkeydown={(e) => {
      if (e.key === 'Enter' && !(e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        onenter?.();
      }
    }}
  />
  <button
    type="button"
    class="btn ghost sm icon"
    aria-label={shown ? t('password.hide') : t('password.show')}
    title={shown ? t('password.hide') : t('password.show')}
    onclick={() => (shown = !shown)}
  >
    <Icon name={shown ? 'eyeOff' : 'eye'} />
  </button>
</div>

<style>
  .pw {
    position: relative;
    display: flex;
    align-items: center;
    width: 100%;
  }
  .pw .input {
    padding-right: 38px;
  }
  .pw button {
    position: absolute;
    right: 4px;
  }
  .invalid .input {
    border-color: var(--danger);
  }
</style>
