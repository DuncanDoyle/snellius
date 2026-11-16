/** User-facing copy of the shell chrome. The banner wording was picked from five options for plain, accurate text. */
export const shellCopy = {
  localMode: "Local mode: no sign-in. Anyone with port-forward access sees everything Snellius can read.",
  /** Secure mode, for a viewer who cannot list every kind everywhere (design §7.5). */
  permissionsNote: "You see what your Kubernetes permissions allow.",
  /** Next to an install's neutral name when the viewer cannot read its metadata sources (design §7.4). */
  detailsHidden: "Details not visible to you",
  /** The header's namespace scope (design §11). Only "All namespaces" is fixed; the rest await the wording picker. */
  namespaceScope: {
    all: "All namespaces",
    /** Accessible name of the button and its panel, and the tooltip's lead. */
    label: "Namespaces",
    /** The button's text when more than one namespace is chosen. */
    count: (n: number) => `${n} namespaces`,
    /** Typed entry: placeholder and accessible name. */
    placeholder: "Add a namespace",
    inputLabel: "Namespace name",
    /** Under the typed entry when the name is not a namespace name. */
    invalid: "Use lowercase letters, digits and dashes",
    /** Under the typed entry when the scope holds the most names the server takes. */
    full: "You can choose up to 100 namespaces",
    /** The choice list when the server lists no namespace and none is chosen. */
    empty: "No namespaces found",
    /** A list's empty state under a scope, and its action that clears the scope. */
    emptyList: (many: string) => `No ${many} in the chosen namespaces.`,
    showAll: "Show all namespaces",
  },
};
