/**
 * The install tabs as data. Enabling a package manager later is a one-line
 * change: flip its `status` to 'available' and replace its blocks with the
 * real commands. Everything shown here exists in the repository today:
 *
 * - the install scripts are scripts/install.sh and scripts/install.ps1;
 * - the archive names follow archives.name_template and format_overrides in
 *   .goreleaser.yaml (brooom_<version>_<os>_<arch>, zip on Windows) and the
 *   checksums file is checksum.name_template there;
 * - the go install path is the module in go.mod plus ./cmd/brooom.
 *
 * Nothing else may be shown as a working command.
 */
import { REPO_URL } from './url';

export type TabStatus = 'available' | 'soon';

/** One piece of panel content, rendered in order. */
export type Block =
  | { type: 'text'; text: string }
  | { type: 'command'; os?: string; command: string; copyLabel: string }
  | { type: 'link'; href: string; label: string }
  | { type: 'list'; items: string[] };

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
    {
      type: 'link',
      href: `${REPO_URL}/blob/main/docs/SPEC.md#platforms-and-distribution`,
      label: 'Platforms and distribution in the spec',
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
        text: 'Downloads the latest release for your platform, verifies its sha256 checksum and installs it without sudo, together with the short command br (unless something else already uses br, in which case it tells you). It needs a published release, and v0.1.0 is not out yet.',
      },
      { type: 'command', os: 'macOS / Linux', command: `curl -fsSL ${RAW}/install.sh | sh`, copyLabel: 'Copy macOS and Linux install command' },
      { type: 'command', os: 'Windows (PowerShell)', command: `irm ${RAW}/install.ps1 | iex`, copyLabel: 'Copy Windows install command' },
    ],
  },
  {
    id: 'release',
    label: 'GitHub release',
    status: 'available',
    blocks: [
      {
        type: 'text',
        text: 'Download the archive for your platform from the latest release, then verify it against the published checksums.txt (sha256) before unpacking.',
      },
      { type: 'link', href: `${REPO_URL}/releases/latest`, label: 'Latest release on GitHub' },
      {
        type: 'list',
        items: [
          'Windows: brooom_<version>_windows_amd64.zip, brooom_<version>_windows_arm64.zip',
          'macOS: brooom_<version>_darwin_amd64.tar.gz, brooom_<version>_darwin_arm64.tar.gz',
          'Linux: brooom_<version>_linux_amd64.tar.gz, brooom_<version>_linux_arm64.tar.gz',
        ],
      },
    ],
  },
  {
    id: 'go',
    label: 'go install',
    status: 'available',
    blocks: [
      { type: 'text', text: 'With Go installed, build and install the latest version from source. This installs brooom only, without the br short command:' },
      {
        type: 'command',
        command: 'go install github.com/Tobias-Braun/brooom/cmd/brooom@latest',
        copyLabel: 'Copy go install command',
      },
    ],
  },
  { id: 'homebrew', label: 'Homebrew', status: 'soon', blocks: soon('Homebrew') },
  { id: 'scoop', label: 'Scoop', status: 'soon', blocks: soon('Scoop') },
  { id: 'winget', label: 'winget', status: 'soon', blocks: soon('winget') },
  { id: 'aur', label: 'AUR', status: 'soon', blocks: soon('AUR') },
  { id: 'packages', label: 'deb / rpm', status: 'soon', blocks: soon('deb and rpm package') },
];

/** localStorage key remembering the last selected tab (best effort only). */
export const TAB_STORAGE_KEY = 'brooom.install-tab';
