/**
 * The install tabs as data. Enabling a package manager later is a one-line
 * change: flip its `status` to 'available' and replace its blocks with the
 * real commands. Everything shown here exists in the repository today:
 *
 * - the install scripts are scripts/install.sh and scripts/install.ps1;
 * - the Homebrew cask is homebrew_casks in .goreleaser.yaml, published to
 *   the Tobias-Braun/homebrew-tap repository.
 *
 * Nothing else may be shown as a working command.
 */
export type TabStatus = 'available' | 'soon';

/** One piece of panel content, rendered in order. */
export type Block =
  | { type: 'text'; text: string }
  | { type: 'command'; os?: string; command: string; copyLabel: string };

export interface InstallTab {
  id: string;
  label: string;
  status: TabStatus;
  blocks: Block[];
}

const RAW = 'https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts';

/** Shared explanation for every manager that is prepared but not enabled. */
function soon(manager: string): Block[] {
  return [
    {
      type: 'text',
      text: `${manager} support is prepared but not enabled yet, so there is no ${manager} command to run today. It will appear here once publishing to it is turned on.`,
    },
  ];
}

export const installTabs: InstallTab[] = [
  {
    id: 'script',
    label: 'Install script',
    status: 'available',
    blocks: [
      {
        type: 'text',
        text: 'Downloads the latest release for your platform, verifies its sha256 checksum and installs it without sudo, together with the short command br (unless something else already uses br, in which case it tells you). It needs a published release, and the first major release is not out yet.',
      },
      { type: 'command', os: 'macOS / Linux', command: `curl -fsSL ${RAW}/install.sh | sh`, copyLabel: 'Copy macOS and Linux install command' },
      { type: 'command', os: 'Windows (PowerShell)', command: `irm ${RAW}/install.ps1 | iex`, copyLabel: 'Copy Windows install command' },
    ],
  },
  {
    id: 'homebrew',
    label: 'Homebrew',
    status: 'available',
    blocks: [
      {
        type: 'text',
        text: 'On macOS, install the cask from the brooom tap. It also adds the short command br when nothing else uses it, and tells you why when it skips it.',
      },
      {
        type: 'command',
        command: 'brew install --cask Tobias-Braun/tap/brooom',
        copyLabel: 'Copy Homebrew install command',
      },
    ],
  },
  { id: 'winget', label: 'winget', status: 'soon', blocks: soon('winget') },
  { id: 'aur', label: 'AUR', status: 'soon', blocks: soon('AUR') },
  { id: 'packages', label: 'deb / rpm', status: 'soon', blocks: soon('deb and rpm package') },
];

/** localStorage key remembering the last selected tab (best effort only). */
export const TAB_STORAGE_KEY = 'brooom.install-tab';
