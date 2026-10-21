import { useEffect, useState } from 'react'
import { Switch } from '@mui/material'
import CloseRoundedIcon from '@mui/icons-material/CloseRounded'
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined'
import VisibilityOffOutlinedIcon from '@mui/icons-material/VisibilityOffOutlined'
import VisibilityOutlinedIcon from '@mui/icons-material/VisibilityOutlined'

import ExtensionTokenSettings from '@/features/auth/components/ExtensionTokenSettings'
import DirectoryManager from '@/features/directories/components/DirectoryManager'
import AppModal from '@/shared/ui/AppModal'
import PlayerSettingsModal from '@/features/settings/components/PlayerSettingsModal'
import WebHotkeySettings from '@/features/settings/components/WebHotkeySettings'
import ProviderAvailabilityPanel from '@/features/settings/components/ProviderAvailabilityPanel'
import ResourceDashboard from '@/features/settings/components/ResourceDashboard'
import TitleTranslationToolSettings from '@/features/settings/components/TitleTranslationToolSettings'
import { downloadFFmpeg, fetchTools } from '@/features/settings/api'
import { parsePlayerHotkeys } from '@/utils/playerHotkeys'
import { zh } from '@/utils/i18n'
import { getErrorMessage } from '@/utils/errors'

const SETTINGS_SECTIONS = [
  {
    id: 'directories',
    title: { zh: '目录管理', en: 'Directory Management' },
    summary: { zh: '管理扫描目录与路径', en: 'Manage watched folders and paths' },
  },
  {
    id: 'display',
    title: { zh: '显示与交互', en: 'Display & Interaction' },
    summary: { zh: '界面提示与交互行为', en: 'Interface hints and interactions' },
  },
  {
    id: 'network',
    title: { zh: '网络与代理', en: 'Network & Proxy' },
    summary: { zh: '网络连接与代理设置', en: 'Network connection and proxy settings' },
  },
  {
    id: 'jav-providers',
    title: { zh: 'JAV 数据源', en: 'JAV Providers' },
    summary: { zh: '数据源域名与可用性检测', en: 'Provider domains and availability checks' },
  },
  {
    id: 'ai-translation',
    title: { zh: 'AI 翻译', en: 'AI Translation' },
    summary: {
      zh: '自动翻译、服务商配置与批量任务',
      en: 'Automatic translation, provider configuration and batch tasks',
    },
  },
  {
    id: 'tools',
    title: { zh: '工具', en: 'Tools' },
    summary: { zh: '下载与管理运行工具', en: 'Download and manage runtime tools' },
  },
  {
    id: 'player',
    title: { zh: '播放器', en: 'Player' },
    summary: { zh: '播放器快捷键与播放控制', en: 'Player shortcuts and playback controls' },
  },
  {
    id: 'security',
    title: { zh: '安全', en: 'Security' },
    summary: { zh: '账户与 API 令牌', en: 'Account and API tokens' },
  },
  {
    id: 'resources',
    title: { zh: '资源监控', en: 'Resource Monitor' },
    summary: { zh: 'CPU、内存与磁盘占用', en: 'CPU, memory and disk usage' },
  },
]

const PLAYER_BASIC_DEFAULTS = {
  windowWidth: 80,
  windowHeight: 80,
  ontop: false,
  reuseWindow: true,
  resumePlayback: true,
  volume: 70,
  showHotkeyHint: true,
}

const BROWSER_PLAYER_DEFAULTS = {
  resumePlayback: true,
  showHotkeyHint: true,
}

// 自动整理的整理方式，与后端 JAVBOSS_AUTO_PROCESS 的取值一一对应。
// 这是容器级配置，网页里只展示、不提供修改。
const AUTO_PROCESS_MODE_LABELS = {
  sidecar: {
    zh: '仅生成 NFO 与封面（不移动文件）',
    en: 'Write NFO and covers only (files stay in place)',
  },
  organize: {
    zh: '仅按番号整理目录',
    en: 'Organize into folders by code only',
  },
  organize_with_sidecar: {
    zh: '整理目录并生成 NFO 与封面',
    en: 'Organize, then write NFO and covers',
  },
}

const DEFAULT_PROXY_HOST = '127.0.0.1'

// 把 /nfo/rebuild 返回的统计拼成一行中文说明。
function formatNFORebuildSummary(summary, dryRun) {
  if (!summary) return ''
  const parts = [
    zh(`共检查 ${summary.videos} 个视频`, `Checked ${summary.videos} videos`),
    zh(`空壳 ${summary.shells} 个`, `${summary.shells} shells`),
    zh(`缺失 ${summary.missing} 个`, `${summary.missing} missing`),
    zh(`外部完整 ${summary.preserved} 个`, `${summary.preserved} kept as-is`),
  ]
  parts.push(
    dryRun
      ? zh(
          `可重建 ${Number(summary.shells || 0) + Number(summary.missing || 0)} 个`,
          `${Number(summary.shells || 0) + Number(summary.missing || 0)} rebuildable`
        )
      : zh(`已重建 ${summary.rebuilt} 个`, `${summary.rebuilt} rebuilt`)
  )
  if (summary.no_metadata) {
    parts.push(zh(`缺刮削数据 ${summary.no_metadata} 个`, `${summary.no_metadata} without metadata`))
  }
  if (summary.failed) {
    parts.push(zh(`失败 ${summary.failed} 个`, `${summary.failed} failed`))
  }
  return parts.join('，')
}

// 任务跑起来后，轮询中途的统计拼成一行实时进度。
// 后端每处理 50 个视频回传一次，所以这里只会看到跳跃式增长的计数。
function formatNFORebuildProgress(summary, dryRun) {
  if (!summary) return ''
  const checked = Number(summary.videos) || 0
  if (checked <= 0) return ''
  const head = dryRun
    ? zh(`已检查 ${checked} 个`, `Checked ${checked}`)
    : zh(`已处理 ${checked} 个`, `Processed ${checked}`)
  const tail = dryRun
    ? zh(
        `，空壳 ${Number(summary.shells) || 0} 个、缺失 ${Number(summary.missing) || 0} 个`,
        `, ${Number(summary.shells) || 0} shells, ${Number(summary.missing) || 0} missing`
      )
    : zh(`，已重写 ${Number(summary.rebuilt) || 0} 个`, `, ${Number(summary.rebuilt) || 0} rewritten`)
  return head + tail
}

export default function GlobalSettingsModal({
  open,
  onClose,
  onToast,
  initialSection = '',
  directories,
  browserPlaybackOnly = false,
  desktopIntegrationEnabled = true,
  containerMode = false,
  serverOS = '',
  appVersion = '',
  mpvEnabled = true,
  onCreateDirectory,
  onUpdateDirectory,
  onDeleteDirectory,
  onProcessDirectory,
  onScanDirectory,
  onRefreshDirectories,
  proxyMode,
  proxyHost,
  proxyPort,
  onSaveProxySettings,
  allowLANAccess = false,
  onSaveAllowLANAccess,
  autoProcessEnabled = false,
  autoProcessAvailable = true,
  autoProcessMode = '',
  onSaveAutoProcessEnabled,
  onRebuildNFO,
  nfoRebuildProgress = null,
  embyEnabled = false,
  embyURL = '',
  embyAPIKeySet = false,
  onSaveEmbySettings,
  onTestEmbyConnection,
  defaultPlayer,
  onSaveDefaultPlayer,
  initialViewMode,
  onSaveInitialViewMode,
  playerWindowWidth,
  playerWindowHeight,
  playerOntop,
  playerReuseWindow,
  playerResumePlayback,
  playerVolume,
  playerShowHotkeyHint,
  onSavePlayerBasicSettings,
  browserPlayerResumePlayback,
  browserPlayerShowHotkeyHint,
  onSaveBrowserPlayerSettings,
  playerHotkeys,
  onSavePlayerHotkeys,
  webHotkeys,
  onSaveWebHotkeys,
  onChangePassword,
  onLogout,
}) {
  const [proxyHostInput, setProxyHostInput] = useState('')
  const [proxyInput, setProxyInput] = useState('')
  const [proxyError, setProxyError] = useState('')
  const [savingProxy, setSavingProxy] = useState(false)
  const [proxyEditing, setProxyEditing] = useState(false)
  const currentProxyMode = ['auto', 'direct', 'manual'].includes(proxyMode)
    ? proxyMode
    : proxyPort
      ? 'manual'
      : 'auto'
  const [proxyModeInput, setProxyModeInput] = useState('auto')
  const [allowLANAccessInput, setAllowLANAccessInput] = useState(false)
  const [allowLANAccessError, setAllowLANAccessError] = useState('')
  const [savingAllowLANAccess, setSavingAllowLANAccess] = useState(false)
  const [autoProcessEnabledInput, setAutoProcessEnabledInput] = useState(false)
  const [autoProcessError, setAutoProcessError] = useState('')
  const [savingAutoProcess, setSavingAutoProcess] = useState(false)
  const [nfoRebuildBusy, setNfoRebuildBusy] = useState(false)
  const [nfoRebuildMessage, setNfoRebuildMessage] = useState('')
  const [nfoRebuildError, setNfoRebuildError] = useState('')
  // 当前跑的是统计还是重建，决定实时进度文案的措辞。
  const [nfoRebuildDryRun, setNfoRebuildDryRun] = useState(false)
  // Emby 联动：地址/密钥用受控输入，密钥只在"确实改了"时才随保存一起提交。
  const [embyEnabledInput, setEmbyEnabledInput] = useState(false)
  const [embyURLInput, setEmbyURLInput] = useState('')
  const [embyAPIKeyInput, setEmbyAPIKeyInput] = useState('')
  const [embyError, setEmbyError] = useState('')
  const [embyNotice, setEmbyNotice] = useState('')
  const [savingEmby, setSavingEmby] = useState(false)
  const [testingEmby, setTestingEmby] = useState(false)
  const [activeSection, setActiveSection] = useState('directories')
  const [defaultPlayerInput, setDefaultPlayerInput] = useState('mpv')
  const [defaultPlayerError, setDefaultPlayerError] = useState('')
  const [savingDefaultPlayer, setSavingDefaultPlayer] = useState(false)
  const [initialViewModeInput, setInitialViewModeInput] = useState('video')
  const [initialViewModeError, setInitialViewModeError] = useState('')
  const [savingInitialViewMode, setSavingInitialViewMode] = useState(false)
  const [playerTab, setPlayerTab] = useState('basic')
  const [playerBasicError, setPlayerBasicError] = useState('')
  const [playerBasicSuccess, setPlayerBasicSuccess] = useState('')
  const [savingPlayerBasic, setSavingPlayerBasic] = useState(false)
  const [playerWindowWidthInput, setPlayerWindowWidthInput] = useState('')
  const [playerWindowHeightInput, setPlayerWindowHeightInput] = useState('')
  const [playerOntopInput, setPlayerOntopInput] = useState(false)
  const [playerReuseWindowInput, setPlayerReuseWindowInput] = useState(true)
  const [playerResumePlaybackInput, setPlayerResumePlaybackInput] = useState(true)
  const [playerVolumeInput, setPlayerVolumeInput] = useState('')
  const [playerShowHotkeyHintInput, setPlayerShowHotkeyHintInput] = useState(true)
  const [browserPlayerResumePlaybackInput, setBrowserPlayerResumePlaybackInput] = useState(true)
  const [browserPlayerShowHotkeyHintInput, setBrowserPlayerShowHotkeyHintInput] = useState(true)
  const [browserPlayerError, setBrowserPlayerError] = useState('')
  const [browserPlayerSuccess, setBrowserPlayerSuccess] = useState('')
  const [savingBrowserPlayer, setSavingBrowserPlayer] = useState(false)
  const [passwordDialogOpen, setPasswordDialogOpen] = useState(false)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [visiblePasswords, setVisiblePasswords] = useState({
    current: false,
    new: false,
    confirm: false,
  })
  const [passwordError, setPasswordError] = useState('')
  const [savingPassword, setSavingPassword] = useState(false)
  const [ffmpegStatus, setFFmpegStatus] = useState(null)
  const [toolsLoading, setToolsLoading] = useState(false)
  const [toolsError, setToolsError] = useState('')
  const [startingFFmpegDownload, setStartingFFmpegDownload] = useState(false)

  const normalizedPlayerHotkeys = parsePlayerHotkeys(playerHotkeys)
  const ffmpegInstalledLabel =
    ffmpegStatus?.source === 'builtin' ? zh('已内置', 'Built in') : zh('已安装', 'Installed')

  const resetPlayerBasicInputs = () => {
    setPlayerWindowWidthInput(String(PLAYER_BASIC_DEFAULTS.windowWidth))
    setPlayerWindowHeightInput(String(PLAYER_BASIC_DEFAULTS.windowHeight))
    setPlayerOntopInput(PLAYER_BASIC_DEFAULTS.ontop)
    setPlayerReuseWindowInput(PLAYER_BASIC_DEFAULTS.reuseWindow)
    setPlayerResumePlaybackInput(PLAYER_BASIC_DEFAULTS.resumePlayback)
    setPlayerVolumeInput(String(PLAYER_BASIC_DEFAULTS.volume))
    setPlayerShowHotkeyHintInput(PLAYER_BASIC_DEFAULTS.showHotkeyHint)
    setPlayerBasicError('')
    setPlayerBasicSuccess('')
  }

  useEffect(() => {
    if (open) {
      if (SETTINGS_SECTIONS.some((section) => section.id === initialSection)) {
        setActiveSection(initialSection)
      }
      setPlayerTab('basic')
      setPlayerBasicError('')
      setPlayerBasicSuccess('')
      setBrowserPlayerError('')
      setBrowserPlayerSuccess('')
    }
  }, [initialSection, open])

  useEffect(() => {
    if (open) {
      setProxyHostInput(proxyHost || DEFAULT_PROXY_HOST)
      setProxyInput(proxyPort ? String(proxyPort) : '')
      setProxyModeInput(currentProxyMode)
      setProxyEditing(false)
      setProxyError('')
      setAllowLANAccessInput(allowLANAccess === true)
      setAllowLANAccessError('')
      setAutoProcessEnabledInput(autoProcessEnabled === true)
      setAutoProcessError('')
      // Emby 联动：密钥明文不回传，只回"是否已配置"；输入框每次打开都重置，
      // 避免上一次没保存的密钥残留在界面上。
      setEmbyEnabledInput(embyEnabled === true)
      setEmbyURLInput(embyURL || '')
      setEmbyAPIKeyInput('')
      setEmbyError('')
      setEmbyNotice('')
      setDefaultPlayerInput(
        defaultPlayer === 'browser' || (defaultPlayer === 'system' && desktopIntegrationEnabled)
          ? defaultPlayer
          : 'mpv'
      )
      setDefaultPlayerError('')
      setInitialViewModeInput(initialViewMode === 'jav' ? 'jav' : 'video')
      setInitialViewModeError('')
      setPlayerWindowWidthInput(String(playerWindowWidth ?? PLAYER_BASIC_DEFAULTS.windowWidth))
      setPlayerWindowHeightInput(String(playerWindowHeight ?? PLAYER_BASIC_DEFAULTS.windowHeight))
      setPlayerOntopInput(playerOntop ?? PLAYER_BASIC_DEFAULTS.ontop)
      setPlayerReuseWindowInput(playerReuseWindow ?? PLAYER_BASIC_DEFAULTS.reuseWindow)
      setPlayerResumePlaybackInput(playerResumePlayback ?? PLAYER_BASIC_DEFAULTS.resumePlayback)
      setPlayerVolumeInput(String(playerVolume ?? PLAYER_BASIC_DEFAULTS.volume))
      setPlayerShowHotkeyHintInput(playerShowHotkeyHint ?? PLAYER_BASIC_DEFAULTS.showHotkeyHint)
      setBrowserPlayerResumePlaybackInput(
        browserPlayerResumePlayback ?? BROWSER_PLAYER_DEFAULTS.resumePlayback
      )
      setBrowserPlayerShowHotkeyHintInput(
        browserPlayerShowHotkeyHint ?? BROWSER_PLAYER_DEFAULTS.showHotkeyHint
      )
      setPasswordDialogOpen(false)
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
      setVisiblePasswords({ current: false, new: false, confirm: false })
      setPasswordError('')
    }
  }, [
    open,
    proxyHost,
    proxyPort,
    currentProxyMode,
    allowLANAccess,
    autoProcessEnabled,
    defaultPlayer,
    initialViewMode,
    playerWindowWidth,
    playerWindowHeight,
    playerOntop,
    playerReuseWindow,
    playerResumePlayback,
    playerVolume,
    playerShowHotkeyHint,
    browserPlayerResumePlayback,
    browserPlayerShowHotkeyHint,
    mpvEnabled,
    browserPlaybackOnly,
    desktopIntegrationEnabled,
  ])

  useEffect(() => {
    if (!open || activeSection !== 'tools') return undefined
    let cancelled = false
    setToolsLoading(true)
    setToolsError('')
    fetchTools()
      .then((tools) => {
        if (!cancelled) setFFmpegStatus(tools?.ffmpeg || null)
      })
      .catch((err) => {
        if (!cancelled) setToolsError(getErrorMessage(err))
      })
      .finally(() => {
        if (!cancelled) setToolsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, activeSection])

  useEffect(() => {
    if (!open || activeSection !== 'tools' || !ffmpegStatus?.downloading) return undefined
    let cancelled = false
    const timer = window.setInterval(() => {
      fetchTools()
        .then((tools) => {
          if (!cancelled) setFFmpegStatus(tools?.ffmpeg || null)
        })
        .catch((err) => {
          if (!cancelled) setToolsError(getErrorMessage(err))
        })
    }, 750)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [open, activeSection, ffmpegStatus?.downloading])

  if (!open) return null

  const handleSaveProxy = async () => {
    setProxyError('')
    const host = proxyHostInput.trim()
    const raw = proxyInput.trim()
    let port = 0
    let nextHost = ''
    if (proxyModeInput === 'manual') {
      if (host === '') {
        setProxyError(zh('请输入代理 IP 或主机名', 'Enter a proxy IP or host'))
        return
      }
      if (raw === '') {
        setProxyError(zh('请输入 1-65535 的端口号', 'Enter a port between 1 and 65535'))
        return
      }
      const parsed = /^\d+$/.test(raw) ? Number(raw) : NaN
      if (!Number.isFinite(parsed) || parsed <= 0 || parsed > 65535) {
        setProxyError(zh('请输入 1-65535 的端口号', 'Enter a port between 1 and 65535'))
        return
      }
      port = parsed
      nextHost = host
    }
    setSavingProxy(true)
    try {
      await onSaveProxySettings?.({ mode: proxyModeInput, host: nextHost, port })
      setProxyEditing(false)
    } catch (err) {
      setProxyError(getErrorMessage(err))
    } finally {
      setSavingProxy(false)
    }
  }

  const currentProxyHost = proxyHost || DEFAULT_PROXY_HOST
  const proxyHostInputTrimmed = proxyHostInput.trim()
  const proxyInputTrimmed = proxyInput.trim()
  const desiredHostText = proxyModeInput === 'manual' ? proxyHostInputTrimmed : ''
  const desiredPortText = proxyModeInput === 'manual' ? proxyInputTrimmed : ''
  const currentHostText = currentProxyMode === 'manual' ? currentProxyHost : ''
  const currentPortText = currentProxyMode === 'manual' ? String(proxyPort) : ''
  const proxyUnchanged =
    proxyModeInput === currentProxyMode &&
    desiredHostText === currentHostText &&
    desiredPortText === currentPortText
  const proxyHostMissing = proxyModeInput === 'manual' && proxyHostInputTrimmed === ''
  const proxyInputMissing = proxyModeInput === 'manual' && proxyInputTrimmed === ''
  const visibleSections = SETTINGS_SECTIONS
  const currentSection = visibleSections.some((section) => section.id === activeSection)
    ? activeSection
    : 'directories'
  const activeTitle = visibleSections.find((item) => item.id === currentSection)?.title || {
    zh: '全局设置',
    en: 'Global Settings',
  }

  const handleChangeDefaultPlayer = async (player) => {
    if (savingDefaultPlayer || !onSaveDefaultPlayer) return
    const next =
      player === 'browser' || (player === 'system' && desktopIntegrationEnabled) ? player : 'mpv'
    const previous = defaultPlayerInput
    if (next === previous) return
    setDefaultPlayerInput(next)
    setDefaultPlayerError('')
    setSavingDefaultPlayer(true)
    try {
      await onSaveDefaultPlayer?.(next)
    } catch (err) {
      setDefaultPlayerInput(previous)
      setDefaultPlayerError(getErrorMessage(err))
    } finally {
      setSavingDefaultPlayer(false)
    }
  }

  const handleChangeInitialViewMode = async (mode) => {
    if (savingInitialViewMode || !onSaveInitialViewMode) return
    const next = mode === 'jav' ? 'jav' : 'video'
    const previous = initialViewModeInput
    if (next === previous) return
    setInitialViewModeInput(next)
    setInitialViewModeError('')
    setSavingInitialViewMode(true)
    try {
      await onSaveInitialViewMode?.(next)
    } catch (err) {
      setInitialViewModeInput(previous)
      setInitialViewModeError(getErrorMessage(err))
    } finally {
      setSavingInitialViewMode(false)
    }
  }

  const renderDefaultPlayerSettings = () => {
    return (
      <div className="space-y-4">
        {browserPlaybackOnly ? (
          <div>
            <h4 className="text-sm font-semibold text-zinc-800">
              {zh('默认播放器', 'Default Player')}
            </h4>
            <p className="mt-1 text-sm text-zinc-500">
              {zh(
                'Docker 模式下默认使用网页播放器，如需使用 MPV 请在本机安装 JavBoss 并开启 Client 模式',
                'Docker mode uses the web player by default. To use MPV, install JavBoss on your local machine and enable Client mode.'
              )}
            </p>
          </div>
        ) : (
          <>
            <div className="flex items-center justify-between gap-3">
              <h4 className="text-sm font-semibold text-zinc-800">
                {zh('默认播放器', 'Default Player')}
              </h4>
              <span className="relative inline-block">
                <select
                  value={defaultPlayerInput}
                  onChange={(event) => handleChangeDefaultPlayer(event.target.value)}
                  disabled={savingDefaultPlayer || !onSaveDefaultPlayer}
                  className="w-auto appearance-none rounded-xl border border-zinc-200 bg-white py-1.5 pl-3 pr-7 text-sm text-zinc-800 outline-none focus:border-zinc-200 focus:outline-none focus:ring-0"
                >
                  <option value="mpv">MPV</option>
                  <option value="browser">{zh('网页播放器', 'Web Player')}</option>
                  {desktopIntegrationEnabled ? (
                    <option value="system">{zh('系统播放器', 'System Player')}</option>
                  ) : null}
                </select>
                <span
                  aria-hidden="true"
                  className="pointer-events-none absolute right-4 top-1/2 h-1.5 w-1.5 -translate-y-1/2 rotate-45 border-b border-r border-zinc-500"
                />
              </span>
            </div>
            {defaultPlayerInput === 'mpv' ? (
              <p className="mt-1 text-sm text-zinc-500">
                {zh(
                  '使用 JavBoss 自带的 MPV 播放器进行播放。',
                  'Play with the MPV player bundled with JavBoss.'
                )}
              </p>
            ) : defaultPlayerInput === 'system' ? (
              <p className="mt-1 text-sm text-zinc-500">
                {zh(
                  '使用系统关联的默认程序播放视频，',
                  'Open videos with the system’s default app. '
                )}
                <strong className="font-semibold text-amber-700">
                  {zh('此方式无法统计观看时长。', 'Watch time cannot be tracked. ')}
                </strong>
                {zh(
                  '批量播放需将 M3U8 文件关联到支持播放列表的播放器。',
                  'For bulk playback, associate M3U8 files with a player that supports playlists.'
                )}
              </p>
            ) : defaultPlayerInput === 'browser' ? (
              <p className="mt-1 text-sm text-zinc-500">
                {zh(
                  '网页播放器默认支持的编码格式有限，不支持的格式依赖服务端转码播放，请前往“工具”确认 FFmpeg 已安装。',
                  'The web player supports a limited range of codecs by default. Unsupported formats rely on server-side transcoding for playback. Go to Tools to check that FFmpeg is installed.'
                )}
              </p>
            ) : null}
          </>
        )}

        {defaultPlayerError && (
          <div className="text-sm text-red-600" role="alert">
            {defaultPlayerError}
          </div>
        )}
      </div>
    )
  }

  const renderProxyPanel = () => (
    <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h4 className="text-sm font-semibold text-zinc-800">
            {zh('代理设置', 'Proxy Settings')}
          </h4>
          <button
            type="button"
            onClick={() => {
              setProxyEditing(true)
              setProxyError('')
            }}
            disabled={proxyEditing}
            className={`rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 ${proxyEditing ? 'invisible' : ''}`}
          >
            {zh('修改', 'Edit')}
          </button>
        </div>

        <div className="space-y-3 rounded-2xl bg-zinc-50 px-4 py-3">
          <fieldset
            className="flex flex-wrap gap-x-6 gap-y-3"
            disabled={!proxyEditing || savingProxy}
          >
            <legend className="sr-only">{zh('代理模式', 'Proxy mode')}</legend>
            {[
              { value: 'auto', label: zh('自动检测', 'Auto-detect') },
              { value: 'manual', label: zh('手动设置', 'Manual') },
              { value: 'direct', label: zh('不使用代理', 'No proxy') },
            ].map((option) => (
              <label key={option.value} className="flex items-center gap-2 text-sm text-zinc-700">
                <input
                  type="radio"
                  name="proxy-mode"
                  value={option.value}
                  checked={proxyModeInput === option.value}
                  onChange={() => {
                    setProxyModeInput(option.value)
                    setProxyError('')
                  }}
                  className="h-4 w-4"
                />
                <span>{option.label}</span>
              </label>
            ))}
          </fieldset>

          {proxyModeInput === 'auto' && (
            <p className="text-sm text-zinc-500">
              {zh(
                '自动读取系统代理或环境变量配置。',
                'Automatically use system proxy settings or environment variables.'
              )}
            </p>
          )}
          {proxyModeInput === 'direct' && (
            <p className="text-sm text-zinc-500">
              {zh(
                '直接连接网络，忽略系统代理和环境变量中的代理配置。',
                'Connect directly, ignoring system and environment proxy settings.'
              )}
            </p>
          )}

          {proxyModeInput === 'manual' && (
            <div className="grid max-w-2xl gap-3 sm:grid-cols-[minmax(0,1fr)_160px]">
              <div>
                <label className="mb-1 block text-xs font-medium uppercase tracking-wide text-zinc-500">
                  {zh('代理IP', 'Proxy IP')}
                </label>
                <input
                  value={proxyHostInput}
                  onChange={(e) => setProxyHostInput(e.target.value)}
                  disabled={!proxyEditing || savingProxy}
                  placeholder={DEFAULT_PROXY_HOST}
                  className="w-full rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium uppercase tracking-wide text-zinc-500">
                  {zh('端口号', 'Port')}
                </label>
                <input
                  value={proxyInput}
                  onChange={(e) => setProxyInput(e.target.value)}
                  disabled={!proxyEditing || savingProxy}
                  placeholder={zh('输入 1-65535', 'Enter 1-65535')}
                  inputMode="numeric"
                  className="w-full rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm"
                />
              </div>
            </div>
          )}

          {proxyError && <div className="text-sm text-red-600">{proxyError}</div>}

          {proxyEditing && (
            <div className="flex flex-wrap justify-end gap-2">
              <button
                type="button"
                onClick={() => {
                  setProxyHostInput(proxyHost || DEFAULT_PROXY_HOST)
                  setProxyInput(proxyPort ? String(proxyPort) : '')
                  setProxyModeInput(currentProxyMode)
                  setProxyError('')
                  setProxyEditing(false)
                }}
                disabled={savingProxy}
                className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50"
              >
                {zh('取消', 'Cancel')}
              </button>
              <button
                type="button"
                onClick={handleSaveProxy}
                disabled={savingProxy || proxyUnchanged || proxyHostMissing || proxyInputMissing}
                className="rounded-xl bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-60"
              >
                {savingProxy ? zh('保存中…', 'Saving...') : zh('保存', 'Save')}
              </button>
            </div>
          )}
        </div>
      </div>
    </section>
  )

  const renderLANAccessPanel = () => {
    if (containerMode) return null

    const handleToggle = async (enabled) => {
      if (savingAllowLANAccess) return
      const previous = allowLANAccessInput
      setAllowLANAccessInput(enabled)
      setAllowLANAccessError('')
      setSavingAllowLANAccess(true)
      try {
        await onSaveAllowLANAccess?.(enabled)
      } catch (err) {
        setAllowLANAccessInput(previous)
        setAllowLANAccessError(getErrorMessage(err))
      } finally {
        setSavingAllowLANAccess(false)
      }
    }

    return (
      <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h4 id="lan-access-title" className="text-sm font-semibold text-zinc-800">
              {zh('允许局域网设备访问', 'Allow access from local network devices')}
            </h4>
            <p id="lan-access-description" className="mt-1 text-sm text-zinc-500">
              {zh(
                '开启后，局域网设备可以通过本机 IP 地址访问 JavBoss。',
                'Allow devices on your local network to access JavBoss through this computer’s IP address.'
              )}
            </p>
          </div>
          <Switch
            size="small"
            checked={allowLANAccessInput}
            onChange={(_, checked) => handleToggle(checked)}
            disabled={savingAllowLANAccess || !onSaveAllowLANAccess}
            inputProps={{
              role: 'switch',
              'aria-labelledby': 'lan-access-title',
              'aria-describedby': 'lan-access-description',
            }}
          />
        </div>
        {allowLANAccessError && (
          <div className="mt-3 text-sm text-red-600" role="alert">
            {allowLANAccessError}
          </div>
        )}
      </section>
    )
  }

  const renderNetworkPanel = () => (
    <div className="space-y-5">
      {renderLANAccessPanel()}
      {renderProxyPanel()}
    </div>
  )

  const renderDisplayPanel = () => {
    return (
      <div className="space-y-5">
        <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
          <div className="space-y-4">
            <div className="flex items-center justify-between gap-3">
              <h4 className="text-sm font-semibold text-zinc-800">
                {zh('初始页面', 'Initial Page')}
              </h4>
              <span className="relative inline-block">
                <select
                  value={initialViewModeInput}
                  onChange={(event) => handleChangeInitialViewMode(event.target.value)}
                  disabled={savingInitialViewMode || !onSaveInitialViewMode}
                  className="w-auto appearance-none rounded-xl border border-zinc-200 bg-white py-1.5 pl-3 pr-7 text-sm text-zinc-800 outline-none focus:border-zinc-200 focus:outline-none focus:ring-0"
                >
                  <option value="video">{zh('视频', 'Video')}</option>
                  <option value="jav">JAV</option>
                </select>
                <span
                  aria-hidden="true"
                  className="pointer-events-none absolute right-4 top-1/2 h-1.5 w-1.5 -translate-y-1/2 rotate-45 border-b border-r border-zinc-500"
                />
              </span>
            </div>
            {initialViewModeError && (
              <div className="text-sm text-red-600" role="alert">
                {initialViewModeError}
              </div>
            )}
          </div>
        </section>
        <WebHotkeySettings hotkeys={webHotkeys} onSave={onSaveWebHotkeys} />
      </div>
    )
  }

  const renderPlayerPanel = () => {
    const showMPVSettings = mpvEnabled && !browserPlaybackOnly
    const currentPlayerTab =
      playerTab === 'hotkeys'
        ? 'hotkeys'
        : playerTab === 'browser'
          ? 'browser'
          : showMPVSettings && playerTab === 'mpv'
            ? 'mpv'
            : 'basic'

    return (
      <div className="space-y-5">
        <div className="flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => setPlayerTab('basic')}
            className={`rounded-xl px-3 py-1.5 text-sm ${
              currentPlayerTab === 'basic'
                ? 'bg-zinc-900 text-white'
                : 'border border-zinc-200 bg-white text-zinc-700 hover:bg-zinc-50'
            }`}
          >
            {zh('基础设置', 'Basic Settings')}
          </button>
          <button
            type="button"
            onClick={() => setPlayerTab('browser')}
            className={`rounded-xl px-3 py-1.5 text-sm ${
              currentPlayerTab === 'browser'
                ? 'bg-zinc-900 text-white'
                : 'border border-zinc-200 bg-white text-zinc-700 hover:bg-zinc-50'
            }`}
          >
            {zh('网页播放器', 'Web Player')}
          </button>
          {showMPVSettings ? (
            <button
              type="button"
              onClick={() => setPlayerTab('mpv')}
              className={`rounded-xl px-3 py-1.5 text-sm ${
                currentPlayerTab === 'mpv'
                  ? 'bg-zinc-900 text-white'
                  : 'border border-zinc-200 bg-white text-zinc-700 hover:bg-zinc-50'
              }`}
            >
              {zh('MPV播放器', 'MPV Player')}
            </button>
          ) : null}
          <button
            type="button"
            onClick={() => setPlayerTab('hotkeys')}
            className={`rounded-xl px-3 py-1.5 text-sm ${
              currentPlayerTab === 'hotkeys'
                ? 'bg-zinc-900 text-white'
                : 'border border-zinc-200 bg-white text-zinc-700 hover:bg-zinc-50'
            }`}
          >
            {zh('快捷键', 'Shortcuts')}
          </button>
        </div>
        <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
          {currentPlayerTab === 'basic' ? (
            renderDefaultPlayerSettings()
          ) : currentPlayerTab === 'browser' ? (
            <div>
              <section className="mb-5 space-y-3">
                <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                  <input
                    type="checkbox"
                    checked={browserPlayerResumePlaybackInput}
                    onChange={(e) => {
                      setBrowserPlayerResumePlaybackInput(e.target.checked)
                      setBrowserPlayerError('')
                      setBrowserPlayerSuccess('')
                    }}
                    className="h-4 w-4 rounded"
                  />
                  <span>{zh('从上次结束位置播放', 'Resume From Last Position')}</span>
                </label>
                <p className="text-xs text-zinc-500">
                  {zh(
                    '在当前浏览器中记住每个视频文件的播放位置，下次打开时继续播放。',
                    'Remember each video file’s position in this browser and resume it next time.'
                  )}
                </p>
              </section>
              <section className="space-y-3">
                <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                  <input
                    type="checkbox"
                    checked={browserPlayerShowHotkeyHintInput}
                    onChange={(e) => {
                      setBrowserPlayerShowHotkeyHintInput(e.target.checked)
                      setBrowserPlayerError('')
                      setBrowserPlayerSuccess('')
                    }}
                    className="h-4 w-4 rounded"
                  />
                  <span>{zh('启动时显示快捷键配置', 'Show Shortcuts on Startup')}</span>
                </label>
                <p className="text-xs text-zinc-500">
                  {zh(
                    '在网页播放器打开视频时显示当前快捷键说明。',
                    'Show the current shortcut guide when the web player opens a video.'
                  )}
                </p>
              </section>

              {browserPlayerError && (
                <div className="mt-3 text-sm text-red-600">{browserPlayerError}</div>
              )}
              {browserPlayerSuccess && (
                <div className="mt-3 text-sm text-emerald-600">{browserPlayerSuccess}</div>
              )}

              <div className="mt-4 flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => {
                    setBrowserPlayerResumePlaybackInput(BROWSER_PLAYER_DEFAULTS.resumePlayback)
                    setBrowserPlayerShowHotkeyHintInput(BROWSER_PLAYER_DEFAULTS.showHotkeyHint)
                    setBrowserPlayerError('')
                    setBrowserPlayerSuccess('')
                  }}
                  disabled={savingBrowserPlayer}
                  className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-60"
                >
                  {zh('恢复默认', 'Restore Defaults')}
                </button>
                <button
                  type="button"
                  onClick={async () => {
                    setBrowserPlayerError('')
                    setBrowserPlayerSuccess('')
                    setSavingBrowserPlayer(true)
                    try {
                      await onSaveBrowserPlayerSettings?.({
                        browser_player_resume_playback: browserPlayerResumePlaybackInput,
                        browser_player_show_hotkey_hint: browserPlayerShowHotkeyHintInput,
                      })
                      setBrowserPlayerSuccess(
                        zh('网页播放器设置保存成功', 'Web player settings saved')
                      )
                    } catch (err) {
                      setBrowserPlayerError(getErrorMessage(err))
                    } finally {
                      setSavingBrowserPlayer(false)
                    }
                  }}
                  disabled={savingBrowserPlayer}
                  className="rounded-xl bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-60"
                >
                  {savingBrowserPlayer ? zh('保存中…', 'Saving...') : zh('保存', 'Save')}
                </button>
              </div>
            </div>
          ) : currentPlayerTab === 'mpv' ? (
            <div>
              <div className="space-y-6">
                <section className="space-y-3">
                  <h4 className="text-sm font-semibold text-zinc-800">
                    {zh('初始窗口大小', 'Initial Window Size')}
                  </h4>
                  <div className="flex flex-col gap-3">
                    <div className="flex flex-wrap gap-4">
                      <label className="flex items-center gap-2 text-xs font-medium text-zinc-500">
                        <span className="shrink-0">{zh('宽度', 'Width')}</span>
                        <div className="flex items-center gap-2">
                          <input
                            value={playerWindowWidthInput}
                            onChange={(e) => {
                              setPlayerWindowWidthInput(e.target.value)
                              setPlayerBasicError('')
                              setPlayerBasicSuccess('')
                            }}
                            inputMode="numeric"
                            className="w-28 rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800"
                          />
                          <span className="text-sm text-zinc-500">%</span>
                        </div>
                      </label>

                      <label className="flex items-center gap-2 text-xs font-medium text-zinc-500">
                        <span className="shrink-0">{zh('高度', 'Height')}</span>
                        <div className="flex items-center gap-2">
                          <input
                            value={playerWindowHeightInput}
                            onChange={(e) => {
                              setPlayerWindowHeightInput(e.target.value)
                              setPlayerBasicError('')
                              setPlayerBasicSuccess('')
                            }}
                            inputMode="numeric"
                            className="w-28 rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800"
                          />
                          <span className="text-sm text-zinc-500">%</span>
                        </div>
                      </label>
                    </div>

                    <p className="text-xs text-zinc-500">
                      {zh(
                        '设置 mpv 启动时的宽高占据屏幕宽高的比例。',
                        'Set the percentage of screen width and height used by the mpv window on startup.'
                      )}
                    </p>
                  </div>
                </section>

                <section className="space-y-3 border-t border-zinc-200 pt-5">
                  <div className="flex flex-wrap items-center gap-3">
                    <h4 className="text-sm font-semibold text-zinc-800">
                      {zh('初始音量', 'Initial Volume')}
                    </h4>
                    <div className="flex items-center gap-2">
                      <input
                        value={playerVolumeInput}
                        onChange={(e) => {
                          setPlayerVolumeInput(e.target.value)
                          setPlayerBasicError('')
                          setPlayerBasicSuccess('')
                        }}
                        inputMode="numeric"
                        className="w-28 rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800"
                      />
                      <span className="text-sm text-zinc-500">%</span>
                    </div>
                  </div>
                  <p className="text-xs text-zinc-500">
                    {zh(
                      '控制 mpv 启动时的默认音量，范围 0-130。',
                      'Controls the default mpv startup volume, range 0-130.'
                    )}
                  </p>
                </section>

                <section className="space-y-3 border-t border-zinc-200 pt-5">
                  <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                    <input
                      type="checkbox"
                      checked={playerOntopInput}
                      onChange={(e) => {
                        setPlayerOntopInput(e.target.checked)
                        setPlayerBasicError('')
                        setPlayerBasicSuccess('')
                      }}
                      className="h-4 w-4 rounded"
                    />
                    <span>{zh('播放器强行置顶', 'Keep Player On Top')}</span>
                  </label>
                  <p className="text-xs text-zinc-500">
                    {zh(
                      '开启后，mpv 播放器窗口会保持置顶。',
                      'When enabled, the mpv player window stays on top.'
                    )}
                  </p>
                </section>

                <section className="space-y-3 border-t border-zinc-200 pt-5">
                  <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                    <input
                      type="checkbox"
                      checked={playerReuseWindowInput}
                      onChange={(e) => {
                        setPlayerReuseWindowInput(e.target.checked)
                        setPlayerBasicError('')
                        setPlayerBasicSuccess('')
                      }}
                      className="h-4 w-4 rounded"
                    />
                    <span>
                      {zh(
                        '播放新视频时复用当前播放器窗口',
                        'Reuse Current Player Window When Playing a New Video'
                      )}
                    </span>
                  </label>
                  <p className="text-xs text-zinc-500">
                    {zh(
                      '关闭后，每次播放都会启动新的 mpv 播放器窗口。',
                      'When disabled, each playback starts a new mpv player window.'
                    )}
                  </p>
                </section>

                <section className="space-y-3 border-t border-zinc-200 pt-5">
                  <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                    <input
                      type="checkbox"
                      checked={playerResumePlaybackInput}
                      onChange={(e) => {
                        setPlayerResumePlaybackInput(e.target.checked)
                        setPlayerBasicError('')
                        setPlayerBasicSuccess('')
                      }}
                      className="h-4 w-4 rounded"
                    />
                    <span>{zh('从上次结束位置播放', 'Resume From Last Position')}</span>
                  </label>
                  <p className="text-xs text-zinc-500">
                    {zh(
                      'mpv 会记住每个视频的播放位置，下次播放同一文件时自动恢复。',
                      'mpv remembers each video position and resumes the same file automatically.'
                    )}
                  </p>
                </section>

                <section className="space-y-3 border-t border-zinc-200 pt-5">
                  <label className="flex items-center gap-3 text-sm font-semibold text-zinc-800">
                    <input
                      type="checkbox"
                      checked={playerShowHotkeyHintInput}
                      onChange={(e) => {
                        setPlayerShowHotkeyHintInput(e.target.checked)
                        setPlayerBasicError('')
                        setPlayerBasicSuccess('')
                      }}
                      className="h-4 w-4 rounded"
                    />
                    <span>{zh('启动时显示快捷键配置', 'Show Shortcuts on Startup')}</span>
                  </label>
                  <p className="text-xs text-zinc-500">
                    {zh(
                      '在 mpv 打开视频时显示当前快捷键说明。',
                      'Show the current shortcut guide when mpv opens a video.'
                    )}
                  </p>
                </section>
              </div>

              {playerBasicError && (
                <div className="mt-3 text-sm text-red-600">{playerBasicError}</div>
              )}
              {playerBasicSuccess && (
                <div className="mt-3 text-sm text-emerald-600">{playerBasicSuccess}</div>
              )}

              <div className="mt-4 flex justify-end gap-2">
                <button
                  type="button"
                  onClick={resetPlayerBasicInputs}
                  disabled={savingPlayerBasic}
                  className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-60"
                >
                  {zh('恢复默认', 'Restore Defaults')}
                </button>
                <button
                  type="button"
                  onClick={async () => {
                    setPlayerBasicError('')
                    setPlayerBasicSuccess('')
                    const width = Number.parseInt(playerWindowWidthInput, 10)
                    const height = Number.parseInt(playerWindowHeightInput, 10)
                    const volume = Number.parseInt(playerVolumeInput, 10)
                    if (!Number.isFinite(width) || width < 10 || width > 100) {
                      setPlayerBasicError(
                        zh('初始宽度请输入 10-100', 'Initial width must be between 10 and 100')
                      )
                      return
                    }
                    if (!Number.isFinite(height) || height < 10 || height > 100) {
                      setPlayerBasicError(
                        zh('初始高度请输入 10-100', 'Initial height must be between 10 and 100')
                      )
                      return
                    }
                    if (!Number.isFinite(volume) || volume < 0 || volume > 130) {
                      setPlayerBasicError(
                        zh('初始音量请输入 0-130', 'Initial volume must be between 0 and 130')
                      )
                      return
                    }

                    setSavingPlayerBasic(true)
                    try {
                      await onSavePlayerBasicSettings?.({
                        player_window_width: width,
                        player_window_height: height,
                        player_ontop: playerOntopInput,
                        player_reuse_window: playerReuseWindowInput,
                        player_resume_playback: playerResumePlaybackInput,
                        player_volume: volume,
                        player_show_hotkey_hint: playerShowHotkeyHintInput,
                      })
                      setPlayerBasicSuccess(
                        zh('MPV播放器设置保存成功', 'MPV player settings saved')
                      )
                    } catch (err) {
                      setPlayerBasicError(getErrorMessage(err))
                    } finally {
                      setSavingPlayerBasic(false)
                    }
                  }}
                  disabled={savingPlayerBasic}
                  className="rounded-xl bg-blue-600 px-3 py-1.5 text-sm text-white disabled:opacity-60"
                >
                  {savingPlayerBasic ? zh('保存中…', 'Saving...') : zh('保存', 'Save')}
                </button>
              </div>
            </div>
          ) : (
            <>
              <div className="mb-4">
                <h4 className="text-sm font-semibold text-zinc-800">{zh('快捷键', 'Shortcuts')}</h4>
                <p className="mt-1 text-xs text-zinc-500">
                  {zh(
                    '正数表示增加，负数表示减少。`Space` 和 `Escape` 仍固定用于播放/暂停和关闭播放器。',
                    'Positive numbers increase, negative numbers decrease. `Space` and `Escape` remain reserved for play/pause and close.'
                  )}
                </p>
              </div>
              <PlayerSettingsModal hotkeys={normalizedPlayerHotkeys} onSave={onSavePlayerHotkeys} />
            </>
          )}
        </section>
      </div>
    )
  }

  const renderDirectoriesPanel = () => {
    const modeLabel = AUTO_PROCESS_MODE_LABELS[autoProcessMode]

    const handleToggleAutoProcess = async (enabled) => {
      if (savingAutoProcess || !onSaveAutoProcessEnabled) return
      const previous = autoProcessEnabledInput
      setAutoProcessEnabledInput(enabled)
      setAutoProcessError('')
      setSavingAutoProcess(true)
      try {
        await onSaveAutoProcessEnabled(enabled)
      } catch (err) {
        setAutoProcessEnabledInput(previous)
        setAutoProcessError(getErrorMessage(err))
      } finally {
        setSavingAutoProcess(false)
      }
    }

    const liveNFORebuildProgress = formatNFORebuildProgress(nfoRebuildProgress, nfoRebuildDryRun)

    // Emby 联动：开关切换后立即保存；地址/密钥走「保存」按钮，
    // 密钥输入框有值时才提交，空表示保留已保存的密钥不变。
    const handleToggleEmby = async (enabled) => {
      if (savingEmby || !onSaveEmbySettings) return
      const previous = embyEnabledInput
      setEmbyEnabledInput(enabled)
      setEmbyError('')
      setSavingEmby(true)
      try {
        await onSaveEmbySettings({ jav_emby_enabled: Boolean(enabled) })
      } catch (err) {
        setEmbyEnabledInput(previous)
        setEmbyError(getErrorMessage(err))
      } finally {
        setSavingEmby(false)
      }
    }

    const handleSaveEmby = async () => {
      if (savingEmby || !onSaveEmbySettings) return
      setEmbyError('')
      setEmbyNotice('')
      if (embyEnabledInput && !embyURLInput.trim()) {
        setEmbyError(
          zh('开启联动需要先填写 Emby 地址', 'An Emby server URL is required to enable the integration')
        )
        return
      }
      setSavingEmby(true)
      try {
        const payload = {
          jav_emby_enabled: Boolean(embyEnabledInput),
          jav_emby_url: embyURLInput.trim(),
        }
        if (embyAPIKeyInput.trim() !== '') {
          payload.jav_emby_api_key = embyAPIKeyInput.trim()
        }
        await onSaveEmbySettings(payload)
        setEmbyAPIKeyInput('')
        setEmbyNotice(zh('Emby 联动设置已保存', 'Emby integration settings saved'))
      } catch (err) {
        setEmbyError(getErrorMessage(err))
      } finally {
        setSavingEmby(false)
      }
    }

    const handleTestEmby = async () => {
      if (testingEmby || !onTestEmbyConnection) return
      setEmbyError('')
      setEmbyNotice('')
      setTestingEmby(true)
      try {
        const info = await onTestEmbyConnection({
          url: embyURLInput.trim(),
          apiKey: embyAPIKeyInput.trim(),
        })
        const name = info?.server_name ? `${info.server_name}` : zh('Emby 服务器', 'Emby server')
        const version = info?.version ? ` ${info.version}` : ''
        setEmbyNotice(zh(`连接成功：${name}${version}`, `Connected: ${name}${version}`))
      } catch (err) {
        setEmbyError(getErrorMessage(err))
      } finally {
        setTestingEmby(false)
      }
    }

    const runNFORebuild = async (dryRun) => {
      if (nfoRebuildBusy || !onRebuildNFO) return
      setNfoRebuildBusy(true)
      setNfoRebuildDryRun(dryRun)
      setNfoRebuildError('')
      setNfoRebuildMessage(dryRun ? zh('正在检查…', 'Checking…') : zh('正在重建，整库可能要几分钟…', 'Rebuilding, this may take a few minutes…'))
      try {
        const summary = await onRebuildNFO({ directoryId: 0, dryRun })
        setNfoRebuildMessage(formatNFORebuildSummary(summary, dryRun))
      } catch (err) {
        setNfoRebuildMessage('')
        setNfoRebuildError(getErrorMessage(err))
      } finally {
        setNfoRebuildBusy(false)
      }
    }

    return (
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-xs font-semibold text-zinc-600">
          <InfoOutlinedIcon fontSize="inherit" className="text-[15px]" aria-hidden="true" />
          {zh(
            '添加本地视频目录让 JavBoss 接管，所有内容将自动为您呈现。',
            'No directories yet. Added folders will be scanned automatically.'
          )}
        </div>
        <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
          <DirectoryManager
            open={open}
            directories={directories}
            onCreate={onCreateDirectory}
            onUpdate={onUpdateDirectory}
            onDelete={onDeleteDirectory}
            onProcess={onProcessDirectory}
            onScan={onScanDirectory}
            onRefresh={onRefreshDirectories}
            serverOS={serverOS}
          />
        </section>
        <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h4 id="auto-process-title" className="text-sm font-semibold text-zinc-800">
                {zh('扫描后自动整理', 'Process automatically after scanning')}
              </h4>
              <p id="auto-process-description" className="mt-1 text-sm text-zinc-500">
                {zh(
                  '扫描到新视频后自动写出 NFO 与封面，不需要再手动执行「整理」。关闭后只做扫描和刮削。',
                  'After a scan finds new videos, NFO and cover files are written automatically — no manual “Organize” run needed. When off, scanning and scraping still run.'
                )}
              </p>
              {modeLabel ? (
                <p className="mt-1 text-xs text-zinc-500">
                  {zh('整理方式：', 'Mode: ')}
                  {zh(modeLabel.zh, modeLabel.en)}
                </p>
              ) : null}
              {!autoProcessAvailable ? (
                <p className="mt-1 text-xs text-amber-600">
                  {zh(
                    '容器环境变量 JAVBOSS_AUTO_PROCESS 已关闭该功能，此开关暂时无效。',
                    'The container environment variable JAVBOSS_AUTO_PROCESS disables this feature, so the switch has no effect.'
                  )}
                </p>
              ) : null}
            </div>
            <Switch
              size="small"
              checked={autoProcessEnabledInput}
              onChange={(_, checked) => handleToggleAutoProcess(checked)}
              disabled={savingAutoProcess || !autoProcessAvailable || !onSaveAutoProcessEnabled}
              inputProps={{
                role: 'switch',
                'aria-labelledby': 'auto-process-title',
                'aria-describedby': 'auto-process-description',
              }}
            />
          </div>
          <div className="mt-4 space-y-2 border-t border-zinc-100 pt-4">
            <p className="text-sm text-zinc-500">
              {zh(
                'NFO 维护：把「只有番号、没有剧情简介」的空壳 NFO 用本地刮削数据重写一遍；已经带剧情简介的外来 NFO 不会被覆盖。',
                'NFO maintenance: rewrite shell NFO files that only contain a code and no plot, using the metadata already saved locally. NFO files with a real plot are left untouched.'
              )}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() => runNFORebuild(true)}
                disabled={nfoRebuildBusy || !onRebuildNFO}
                className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50"
              >
                {zh('先统计一下', 'Count first')}
              </button>
              <button
                type="button"
                onClick={() => runNFORebuild(false)}
                disabled={nfoRebuildBusy || !onRebuildNFO}
                className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50"
              >
                {zh('重建空壳 NFO', 'Rebuild shell NFO')}
              </button>
            </div>
            {nfoRebuildBusy ? (
              <div className="space-y-2">
                <div
                  className="h-2 overflow-hidden rounded-full bg-zinc-100"
                  role="progressbar"
                  aria-label={zh('NFO 重建进度', 'NFO rebuild progress')}
                >
                  <div className="nfo-rebuild-bar__fill h-full w-1/4 rounded-full bg-blue-600" />
                </div>
                {liveNFORebuildProgress ? (
                  <p className="text-xs text-zinc-500">{liveNFORebuildProgress}</p>
                ) : null}
              </div>
            ) : null}
            {nfoRebuildMessage ? (
              <p className="text-xs text-zinc-500">{nfoRebuildMessage}</p>
            ) : null}
            {nfoRebuildError ? (
              <div className="text-sm text-red-600" role="alert">
                {nfoRebuildError}
              </div>
            ) : null}
          </div>
          <div className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
            <div className="flex items-start justify-between gap-4">
              <div>
                <h4 className="text-sm font-semibold text-zinc-800">
                  {zh('Emby 联动', 'Emby integration')}
                </h4>
                <p className="mt-1 text-sm text-zinc-500">
                  {zh(
                    '开启后，批量重建 NFO 或自动整理完成后会自动通知 Emby 刷新媒体库，不需要再手动去 Emby 点「刷新元数据」。',
                    'When enabled, JavBoss notifies Emby to refresh the library after an NFO rebuild or automatic processing finishes, so you no longer refresh metadata by hand.'
                  )}
                </p>
              </div>
              <Switch
                size="small"
                checked={embyEnabledInput}
                onChange={(_, checked) => handleToggleEmby(checked)}
                disabled={savingEmby || !onSaveEmbySettings}
              />
            </div>
            <div className="mt-3 space-y-3">
              <div>
                <label htmlFor="emby-url" className="mb-1.5 block text-sm font-medium text-zinc-700">
                  {zh('Emby 地址', 'Emby server URL')}
                </label>
                <input
                  id="emby-url"
                  type="text"
                  value={embyURLInput}
                  onChange={(event) => {
                    setEmbyURLInput(event.target.value)
                    setEmbyError('')
                    setEmbyNotice('')
                  }}
                  placeholder="http://192.168.1.10:8096"
                  className="w-full rounded-xl border border-zinc-200 bg-white py-2 pl-3 pr-3 text-sm text-zinc-900 outline-none focus:border-blue-500 focus:ring-2 focus:ring-blue-100"
                />
              </div>
              <div>
                <label htmlFor="emby-api-key" className="mb-1.5 block text-sm font-medium text-zinc-700">
                  {zh('API Key', 'API key')}
                </label>
                <input
                  id="emby-api-key"
                  type="password"
                  value={embyAPIKeyInput}
                  onChange={(event) => {
                    setEmbyAPIKeyInput(event.target.value)
                    setEmbyError('')
                    setEmbyNotice('')
                  }}
                  placeholder={
                    embyAPIKeySet
                      ? zh('已保存，留空则不修改', 'Saved — leave empty to keep')
                      : zh('Emby 控制台 → API 密钥 → 新建', 'Emby dashboard → API keys → New')
                  }
                  className="w-full rounded-xl border border-zinc-200 bg-white py-2 pl-3 pr-3 text-sm text-zinc-900 outline-none focus:border-blue-500 focus:ring-2 focus:ring-blue-100"
                />
                <p className="mt-1 text-xs text-zinc-500">
                  {zh(
                    '在 Emby 控制台 → 设置 → API 密钥里新建一个即可。密钥只保存在本机数据库，不会显示在页面上。',
                    'Create one in the Emby dashboard under Settings → API keys. The key is stored locally and never shown on this page.'
                  )}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={handleSaveEmby}
                  disabled={savingEmby || !onSaveEmbySettings}
                  className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50"
                >
                  {zh('保存', 'Save')}
                </button>
                <button
                  type="button"
                  onClick={handleTestEmby}
                  disabled={testingEmby || !onTestEmbyConnection}
                  className="rounded-xl border border-zinc-200 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50"
                >
                  {testingEmby ? zh('正在连接…', 'Connecting…') : zh('测试连接', 'Test connection')}
                </button>
              </div>
              {embyNotice ? <p className="text-xs text-green-700">{embyNotice}</p> : null}
              {embyError ? (
                <div className="text-sm text-red-600" role="alert">
                  {embyError}
                </div>
              ) : null}
            </div>
          </div>
          {autoProcessError && (
            <div className="mt-3 text-sm text-red-600" role="alert">
              {autoProcessError}
            </div>
          )}
        </section>
      </div>
    )
  }

  const renderToolsPanel = () => {
    const installed = Boolean(ffmpegStatus?.installed)
    const upgradeAvailable = Boolean(ffmpegStatus?.upgrade_available)
    const downloading = Boolean(ffmpegStatus?.downloading)
    const supported = ffmpegStatus?.supported !== false
    const progress = Number(ffmpegStatus?.progress) || 0

    const handleDownload = async () => {
      setStartingFFmpegDownload(true)
      setToolsError('')
      try {
        const status = await downloadFFmpeg()
        setFFmpegStatus(status)
      } catch (err) {
        setToolsError(getErrorMessage(err))
      } finally {
        setStartingFFmpegDownload(false)
      }
    }

    return (
      <div className="space-y-5">
        <section className="rounded-2xl border border-zinc-200 bg-white p-5 shadow-sm">
          <div className="flex flex-col gap-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <h4 className="text-sm font-semibold text-zinc-900">FFmpeg</h4>
                  {installed ? (
                    <span className="rounded-full bg-emerald-50 px-2 py-0.5 text-xs font-medium text-emerald-700">
                      {ffmpegInstalledLabel}
                    </span>
                  ) : null}
                </div>
                <p className="mt-2 max-w-2xl text-sm text-zinc-500">
                  {zh(
                    '网页播放器无法直接播放某些视频编码时，JavBoss 使用 FFmpeg 转码后播放。',
                    'When the web player cannot play a video codec directly, JavBoss uses FFmpeg to transcode it for playback.'
                  )}
                </p>
              </div>

              <button
                type="button"
                onClick={handleDownload}
                disabled={
                  toolsLoading || startingFFmpegDownload || downloading || installed || !supported
                }
                className="rounded-xl bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-60"
              >
                {toolsLoading
                  ? zh('检查中…', 'Checking...')
                  : downloading
                    ? zh(`下载中 ${progress}%`, `Downloading ${progress}%`)
                    : installed
                      ? ffmpegInstalledLabel
                      : supported
                        ? upgradeAvailable
                          ? zh('更新 FFmpeg', 'Update FFmpeg')
                          : zh('下载 FFmpeg', 'Download FFmpeg')
                        : zh('当前平台不支持', 'Unsupported platform')}
              </button>
            </div>

            {upgradeAvailable && !downloading ? (
              <div className="rounded-xl border border-blue-100 bg-blue-50 px-4 py-3 text-sm text-blue-700">
                {zh(
                  '检测到 FFmpeg 有新版本，可以立即更新。',
                  'A new FFmpeg version is available. You can update now.'
                )}
              </div>
            ) : null}

            {downloading ? (
              <div>
                <div className="h-2 overflow-hidden rounded-full bg-zinc-100">
                  <div
                    className="h-full rounded-full bg-blue-600 transition-[width] duration-300"
                    style={{ width: `${Math.max(1, Math.min(100, progress))}%` }}
                  />
                </div>
                <p className="mt-2 text-xs text-zinc-500">
                  {zh(
                    '正在下载并校验 FFmpeg，请不要关闭 JavBoss。',
                    'Downloading and validating FFmpeg. Keep JavBoss running.'
                  )}
                </p>
              </div>
            ) : null}

            {ffmpegStatus?.error ? (
              <div className="text-sm text-red-600">
                {zh('下载失败：', 'Download failed: ')}
                {ffmpegStatus.error}
              </div>
            ) : null}
            {toolsError ? <div className="text-sm text-red-600">{toolsError}</div> : null}
          </div>
        </section>
      </div>
    )
  }

  const renderAccountPanel = () => {
    const handleChangePassword = async (event) => {
      event.preventDefault()
      setPasswordError('')
      const newPasswordLength = [...newPassword].length
      const newPasswordBytes = new TextEncoder().encode(newPassword).length
      if (newPasswordLength < 6 || newPasswordLength > 20 || newPasswordBytes > 72) {
        setPasswordError(zh('新密码需为 6-20 个字符', 'New password must be 6-20 characters'))
        return
      }
      if (newPassword !== newPassword.trim()) {
        setPasswordError(
          zh('新密码首尾不能包含空格', 'New password cannot start or end with spaces')
        )
        return
      }
      if (newPassword !== confirmPassword) {
        setPasswordError(zh('两次输入的新密码不一致', 'The new passwords do not match'))
        return
      }
      setSavingPassword(true)
      try {
        await onChangePassword?.(currentPassword, newPassword)
        setCurrentPassword('')
        setNewPassword('')
        setConfirmPassword('')
        setVisiblePasswords({ current: false, new: false, confirm: false })
        setPasswordDialogOpen(false)
      } catch (err) {
        setPasswordError(getErrorMessage(err))
      } finally {
        setSavingPassword(false)
      }
    }

    const openPasswordDialog = () => {
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
      setVisiblePasswords({ current: false, new: false, confirm: false })
      setPasswordError('')
      setPasswordDialogOpen(true)
    }

    const closePasswordDialog = () => {
      if (savingPassword) return
      setPasswordDialogOpen(false)
      setCurrentPassword('')
      setNewPassword('')
      setConfirmPassword('')
      setVisiblePasswords({ current: false, new: false, confirm: false })
      setPasswordError('')
    }

    return (
      <>
        <section className="flex items-center justify-between gap-3 rounded-xl border border-zinc-200 bg-white p-4">
          <h3 className="shrink-0 text-sm font-semibold text-zinc-900">{zh('账户', 'Account')}</h3>
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={openPasswordDialog}
              className="whitespace-nowrap rounded-xl border border-zinc-200 bg-white px-4 py-2 text-sm font-medium text-zinc-700 hover:bg-zinc-50"
            >
              {zh('修改密码', 'Change password')}
            </button>
            <button
              type="button"
              onClick={() => onLogout?.()}
              className="whitespace-nowrap rounded-xl border border-red-200 bg-white px-4 py-2 text-sm font-medium text-red-600 hover:bg-red-50"
            >
              {zh('退出登录', 'Sign out')}
            </button>
          </div>
        </section>

        {passwordDialogOpen ? (
          <AppModal
            ariaLabelledby="change-password-title"
            className="px-4"
            closeDisabled={savingPassword}
            contentClassName="w-full max-w-md rounded-2xl border border-zinc-200 bg-white p-6 shadow-2xl"
            contentComponent="form"
            contentProps={{ onSubmit: handleChangePassword }}
            onClose={closePasswordDialog}
            zIndex={1400}
          >
            <div className="mb-5 flex items-center justify-between gap-4">
              <h3 id="change-password-title" className="text-lg font-semibold text-zinc-900">
                {zh('修改密码', 'Change password')}
              </h3>
              <button
                type="button"
                onClick={closePasswordDialog}
                disabled={savingPassword}
                className="rounded-lg px-2 py-1 text-zinc-500 hover:bg-zinc-100 disabled:opacity-50"
                aria-label={zh('关闭', 'Close')}
              >
                ✕
              </button>
            </div>

            <div className="space-y-4">
              {[
                {
                  id: 'current-password',
                  label: zh('旧密码', 'Current password'),
                  value: currentPassword,
                  setter: setCurrentPassword,
                  autoComplete: 'current-password',
                  visibilityKey: 'current',
                },
                {
                  id: 'new-password',
                  label: zh('新密码', 'New password'),
                  value: newPassword,
                  setter: setNewPassword,
                  autoComplete: 'new-password',
                  visibilityKey: 'new',
                },
                {
                  id: 'confirm-password',
                  label: zh('确认新密码', 'Confirm new password'),
                  value: confirmPassword,
                  setter: setConfirmPassword,
                  autoComplete: 'new-password',
                  visibilityKey: 'confirm',
                },
              ].map((field) => (
                <div key={field.id}>
                  <label
                    htmlFor={field.id}
                    className="mb-1.5 block text-sm font-medium text-zinc-700"
                  >
                    {field.label}
                  </label>
                  <div className="relative">
                    <input
                      id={field.id}
                      type={visiblePasswords[field.visibilityKey] ? 'text' : 'password'}
                      autoComplete={field.autoComplete}
                      value={field.value}
                      onChange={(event) => {
                        field.setter(event.target.value)
                        setPasswordError('')
                      }}
                      className="w-full rounded-xl border border-zinc-200 bg-white py-2 pl-3 pr-16 text-sm text-zinc-900 outline-none focus:border-blue-500 focus:ring-2 focus:ring-blue-100"
                    />
                    <button
                      type="button"
                      onClick={() =>
                        setVisiblePasswords((current) => ({
                          ...current,
                          [field.visibilityKey]: !current[field.visibilityKey],
                        }))
                      }
                      className="absolute right-2 top-1/2 flex -translate-y-1/2 items-center justify-center rounded-md p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700"
                      aria-label={
                        visiblePasswords[field.visibilityKey]
                          ? zh(`隐藏${field.label}`, `Hide ${field.label.toLowerCase()}`)
                          : zh(`显示${field.label}`, `Show ${field.label.toLowerCase()}`)
                      }
                    >
                      {visiblePasswords[field.visibilityKey] ? (
                        <VisibilityOutlinedIcon fontSize="small" aria-hidden="true" />
                      ) : (
                        <VisibilityOffOutlinedIcon fontSize="small" aria-hidden="true" />
                      )}
                    </button>
                  </div>
                </div>
              ))}
            </div>

            {passwordError ? (
              <div className="mt-4 text-sm text-red-600">{passwordError}</div>
            ) : null}

            <div className="mt-6 flex justify-end gap-2">
              <button
                type="button"
                onClick={closePasswordDialog}
                disabled={savingPassword}
                className="rounded-xl border border-zinc-200 bg-white px-4 py-2 text-sm text-zinc-700 hover:bg-zinc-50 disabled:opacity-50"
              >
                {zh('取消', 'Cancel')}
              </button>
              <button
                type="submit"
                disabled={savingPassword || !currentPassword || !newPassword || !confirmPassword}
                className="rounded-xl bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-60"
              >
                {savingPassword ? zh('保存中…', 'Saving...') : zh('确认修改', 'Change password')}
              </button>
            </div>
          </AppModal>
        ) : null}
      </>
    )
  }

  return (
    <AppModal
      ariaLabelledby="global-settings-title"
      className="px-4"
      contentClassName="flex h-[min(86vh,820px)] w-full max-w-6xl flex-col overflow-hidden rounded-[28px] border border-zinc-200 bg-[#f5f5f7] shadow-2xl"
      onClose={onClose}
    >
      <div className="flex shrink-0 items-center justify-between border-b border-zinc-200 bg-white/70 px-6 py-4 backdrop-blur">
        <div>
          <h2 id="global-settings-title" className="text-lg font-semibold text-zinc-900">
            {zh('全局设置', 'Global Settings')}
          </h2>
          <p className="mt-1 text-sm text-zinc-500">{zh(activeTitle.zh, activeTitle.en)}</p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label={zh('关闭全局设置', 'Close global settings')}
          title={zh('关闭', 'Close')}
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-zinc-500 transition hover:bg-zinc-100 hover:text-zinc-800 focus:outline-none"
        >
          <CloseRoundedIcon sx={{ fontSize: 20 }} />
        </button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <aside className="flex min-h-0 shrink-0 flex-col border-b border-zinc-200 bg-white/60 p-3 backdrop-blur md:w-[280px] md:border-b-0 md:border-r">
          <div className="flex min-h-0 gap-2 overflow-x-auto md:flex-1 md:flex-col md:overflow-y-auto">
            {visibleSections.map((section) => {
              const selected = currentSection === section.id
              const badgeText = section.id === 'directories' ? String(directories.length) : ''

              return (
                <button
                  key={section.id}
                  type="button"
                  onClick={() => setActiveSection(section.id)}
                  className={`min-w-[220px] shrink-0 rounded-2xl border px-4 py-3 text-left transition md:min-w-0 ${
                    selected
                      ? 'border-zinc-200 bg-white shadow-sm'
                      : 'border-transparent bg-transparent hover:border-zinc-200 hover:bg-white/80'
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <div className="text-sm font-semibold text-zinc-900">
                        {zh(section.title.zh, section.title.en)}
                      </div>
                    </div>
                    {badgeText ? (
                      <span className="rounded-full bg-zinc-100 px-2 py-0.5 text-xs font-medium text-zinc-600">
                        {badgeText}
                      </span>
                    ) : null}
                  </div>
                </button>
              )
            })}
          </div>
          <div className="hidden shrink-0 px-4 pb-1 pt-4 text-xs text-zinc-500 md:block">
            {zh('当前版本', 'Version')}：
            {appVersion === 'dev' ? zh('开发版', 'Development') : appVersion || '—'}
          </div>
        </aside>

        <section
          className={`min-h-0 flex-1 overflow-y-auto px-4 pb-4 md:px-6 md:pb-6 ${
            currentSection === 'ai-translation'
              ? 'pt-2 md:pt-2'
              : currentSection === 'directories'
                ? 'pt-4 md:pt-3'
                : 'pt-4 md:pt-6'
          }`}
        >
          {currentSection === 'display' && renderDisplayPanel()}
          {currentSection === 'resources' && <ResourceDashboard />}
          {currentSection === 'network' && renderNetworkPanel()}
          {currentSection === 'jav-providers' && (
            <ProviderAvailabilityPanel
              key={`${currentProxyMode}:${proxyHost || ''}:${proxyPort || 0}`}
              disabled={proxyEditing || savingProxy}
            />
          )}
          {currentSection === 'ai-translation' && <TitleTranslationToolSettings />}
          {currentSection === 'tools' && renderToolsPanel()}
          {currentSection === 'player' && renderPlayerPanel()}
          {currentSection === 'directories' && renderDirectoriesPanel()}
          {currentSection === 'security' && (
            <div className="space-y-6">
              <ExtensionTokenSettings onToast={onToast} />
              {renderAccountPanel()}
            </div>
          )}
        </section>
      </div>
      <div className="shrink-0 border-t border-zinc-200 bg-white/60 px-6 py-3 text-xs text-zinc-500 md:hidden">
        {zh('当前版本', 'Version')}：
        {appVersion === 'dev' ? zh('开发版', 'Development') : appVersion || '—'}
      </div>
    </AppModal>
  )
}
