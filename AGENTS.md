# AGENTS

## Commit Message Convention

Use scoped commit subjects plus a short body for non-trivial changes.

Preferred format:

```text
<scope>: <what changed>

<summary of the change>
<why the change was needed>
```

Examples from this repository:

- `Makefile: fix headlamp helm repo URL and remove duplicate repo-add from deploy-headlamp`
- `deploy/headlamp: fix kubeconfig path via config.extraArgs`
- `headlamp-plugin/kcp: redesign API bindings page and workspace UI`
- `headlamp-plugin, deploy/headlamp: add KCP plugin and static kubeconfigs`

Guidelines:

- Start with the primary file, directory, or subsystem affected.
- Use lowercase after the colon unless a proper noun requires capitalization.
- The subject should be descriptive, but the body should carry the real summary.
- Add a short body when the change is anything beyond a trivial one-liner.
- In the body, state both what changed and why it was necessary.
- Make the why explicit: describe the failure, constraint, or behavior that motivated the change.
- Prefer concrete explanations over generic summaries like `fix deploy issue`.
- If multiple areas are changed, list the relevant scopes separated by commas.
- Avoid generic subjects like `fix stuff` or overly short summaries.
- Wrap prose at 80 characters maximum in commit bodies and repository docs unless an
  existing file clearly uses a different convention.

Recommended body structure:

- First sentence: what changed.
- Second sentence: why it changed, including the failure mode or intended behavior.

Example:

```text
Makefile, AGENTS: fix headlamp deploy ordering and document commit message convention

Create the headlamp namespace before applying the plugin ConfigMap and run
the Headlamp kubeconfig bootstrap after the Helm release creates its
service account.

This fixes deploy failures caused by creating namespaced resources before
the namespace existed and by minting a token for a service account that
had not been created yet.
```
