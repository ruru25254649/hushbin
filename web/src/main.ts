import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import { detectLang, setLang, type Lang } from './lib/i18n/index.svelte';
import { prefs, applyTheme, loadServerConfig } from './lib/ui.svelte';

applyTheme();
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme);
loadServerConfig();

const lang = (prefs.lang as Lang | null) ?? detectLang();
setLang(lang).finally(() => {
  mount(App, { target: document.getElementById('app')! });
});

if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {});
  });
}
