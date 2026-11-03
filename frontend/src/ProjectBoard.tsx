import { useEffect, useMemo, useRef, useState } from 'react'
import {
  closestCorners,
  DndContext,
  DragEndEvent,
  DragOverEvent,
  DragOverlay,
  DragStartEvent,
  DraggableSyntheticListeners,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  useDroppable,
  useSensor,
  useSensors,
} from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import ChatPage from './ChatPage'

export type Project = { id: number; name: string; description: string; owner_id?: number; owner_username?: string }
export type Issue = {
  id: number
  project_id: number
  title: string
  description: string
  priority: string
  status: string
  position: number
  assignee_id?: number | null
  reporter_id?: number
  start_date?: string | null
  due_date?: string | null
  assigned_at?: string | null
  started_at?: string | null
  completed_at?: string | null
}
export type ProjectMember = { user_id: number; role: 'admin' | 'member'; username?: string; active?: boolean; synthetic?: boolean }
export type UserSuggestion = { id: number; username: string }
export type UserDirectoryPage = { users: UserSuggestion[]; next_after_id: number | null }
export type ActivityEntry = { id: number; project_id: number; issue_id: number; actor_id: number; event_type: string; payload: Record<string, unknown>; occurred_at: string }
export type AnalyticsSnapshot = { project_id: number; total: number; by_status: Record<string, number>; by_priority: Record<string, number>; open_high_priority: number }
export type AdminUser = { id: number; email: string; role: 'admin' | 'member'; created_at: string; password_reset_required: boolean; active: boolean }

const columns = [
  { id: 'todo', label: 'To do', tone: 'todo' },
  { id: 'in_progress', label: 'In progress', tone: 'progress' },
  { id: 'done', label: 'Done', tone: 'done' },
] as const

type Status = (typeof columns)[number]['id']

export type ProjectBoardProps = {
  screen: 'board' | 'projects' | 'calendar' | 'admin' | 'my-issues' | 'activity' | 'analytics' | 'users' | 'chat'
  projects: Project[]
  adminProjects: Project[]
  adminProjectsLoading: boolean
  adminProjectsError: string
  members: ProjectMember[]
  projectRole: string
  globalRole: string
  selectedProject: Project | undefined
  selectedProjectId: number | null
  issues: Issue[]
  calendarIssues: Issue[]
  calendarLoading: boolean
  myIssues: Issue[]
  myIssuesLoading: boolean
  myIssuesError: string
  activityItems: ActivityEntry[]
  activityLoading: boolean
  activityPartialErrors: string[]
  analytics: AnalyticsSnapshot | null
  analyticsLoading: boolean
  analyticsError: string
  adminUsers: AdminUser[]
  adminUsersLoading: boolean
  adminUsersError: string
  adminUsersHasMore: boolean
  managedUserID: number | null
  managedUserProjectIDs: number[]
  managedUserProjectsLoading: boolean
  managedUserProjectsError: string
  currentUserID: number
  loading: boolean
  error: string
  query: string
  statusFilter: string
  priorityFilter: string
  email: string
  onSelectProject: (id: number) => void
  onShowProjects: () => void
  onShowCalendar: () => void
  onShowAdmin: () => void
  onShowMyIssues: () => void
  onShowActivity: () => void
  onShowAnalytics: () => void
  onShowChat: () => void
  onAskTrackerAssistant: (message: string) => Promise<string>
  onShowUsers: () => void
  onLoadMoreUsers: () => void
  onResetUserPassword: (userID: number, temporaryPassword: string) => Promise<void>
  onCreateUser: (email: string, temporaryPassword: string) => Promise<void>
  onSetUserActive: (userID: number, active: boolean) => Promise<void>
  onManageUserProjects: (userID: number) => void
  onToggleUserProject: (userID: number, projectID: number, add: boolean) => Promise<void>
  onOpenBoard: () => void
  onAddProject: () => void
  onCreateProject: (event: React.FormEvent<HTMLFormElement>, memberIDs: number[]) => Promise<boolean>
  onDeleteProject: (project: Project) => Promise<void>
  onDeleteIssue: (issue: Issue) => Promise<void>
  onLoadUserDirectory: (afterID: number | null) => Promise<UserDirectoryPage>
  onAddIssue: (status?: string) => void
  onLogout: () => void
  onDismissError: () => void
  onQueryChange: (value: string) => void
  onStatusFilterChange: (value: string) => void
  onPriorityFilterChange: (value: string) => void
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  onMoveIssue: (issueId: number, status: string, position: number) => void
  onCalendarRangeChange: (from: string, to: string) => void
  onSearchUsers: (prefix: string) => Promise<UserSuggestion[]>
  onAddMember: (userID: number) => Promise<void>
  onRemoveMember: (userID: number) => Promise<void>
  onSetMemberRole: (userID: number, role: 'admin' | 'member') => Promise<void>
  movePending: boolean
  moveToast: string
  onDismissMoveToast: () => void
}

export function ProjectBoard(props: ProjectBoardProps) {
  const [view, setView] = useState<'board' | 'list'>('board')
  const [sortDescending, setSortDescending] = useState(true)
  const [copilotOpen, setCopilotOpen] = useState(false)
  const [addMembersOpen, setAddMembersOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<{ kind: 'project'; project: Project } | { kind: 'issue'; issue: Issue } | null>(null)
  const addMembersTriggerRef = useRef<HTMLButtonElement>(null)
  const copilotTriggerRef = useRef<HTMLButtonElement>(null)

  function closeCopilot() {
    setCopilotOpen(false)
    window.setTimeout(() => copilotTriggerRef.current?.focus(), 0)
  }

  useEffect(() => {
    if (!copilotOpen) return
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') closeCopilot()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [copilotOpen])

  function requestDelete(action: NonNullable<typeof pendingDelete>) {
    setPendingDelete(action)
  }

  async function confirmPendingDelete() {
    const action = pendingDelete
    if (action?.kind === 'project') await props.onDeleteProject(action.project)
    if (action?.kind === 'issue') await props.onDeleteIssue(action.issue)
    setPendingDelete(null)
  }

  function closeAddMembers() {
    setAddMembersOpen(false)
    window.setTimeout(() => addMembersTriggerRef.current?.focus(), 0)
  }

  const sortedIssues = useMemo(() => {
    const priorityRank: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 }
    return [...props.issues].sort((a, b) => {
      const priorityOrder = (priorityRank[a.priority] ?? 4) - (priorityRank[b.priority] ?? 4)
      const order = priorityOrder || a.id - b.id
      return sortDescending ? order : -order
    })
  }, [props.issues, sortDescending])

  const openCount = props.issues.filter(issue => issue.status !== 'done').length
  const completedCount = props.issues.filter(issue => issue.status === 'done').length
  const highCount = props.issues.filter(issue => ['high', 'critical'].includes(issue.priority) && issue.status !== 'done').length

  return (
    <div className="app-shell">
      <Sidebar
        projects={props.projects}
        selectedProjectId={props.selectedProjectId}
        screen={props.screen}
        projectRole={props.projectRole}
        globalRole={props.globalRole}
        email={props.email}
        onSelectProject={props.onSelectProject}
        onShowProjects={props.onShowProjects}
        onShowCalendar={props.onShowCalendar}
        onShowAdmin={props.onShowAdmin}
        onShowMyIssues={props.onShowMyIssues}
        onShowActivity={props.onShowActivity}
        onShowAnalytics={props.onShowAnalytics}
        onShowChat={props.onShowChat}
        onShowUsers={props.onShowUsers}
        showUserManagement={props.globalRole === 'admin'}
        onOpenBoard={props.onOpenBoard}
        onAddProject={props.onAddProject}
        onLogout={props.onLogout}
      />
      <div className="main-area">
        <TopBar projectName={props.selectedProject?.name} section={screenLabel(props.screen)} query={props.query} onQueryChange={props.onQueryChange} onAddIssue={props.onAddIssue} onOpenCopilot={() => setCopilotOpen(true)} copilotTriggerRef={copilotTriggerRef} onShowProjects={props.onShowProjects} onOpenBoard={props.onOpenBoard} onOpenCurrentSection={() => {
          if (props.screen === 'projects') props.onShowProjects()
          else if (props.screen === 'board') props.onOpenBoard()
          else if (props.screen === 'admin') props.onShowAdmin()
          else if (props.screen === 'calendar') props.onShowCalendar()
          else if (props.screen === 'my-issues') props.onShowMyIssues()
          else if (props.screen === 'activity') props.onShowActivity()
          else if (props.screen === 'analytics') props.onShowAnalytics()
          else if (props.screen === 'users') props.onShowUsers()
          else if (props.screen === 'chat') props.onShowChat()
        }} />
        <main className="board-content">
          {props.screen === 'chat' ? <ChatPage onAsk={props.onAskTrackerAssistant} /> : props.screen === 'board' ? <>
          <ProjectHeader
            project={props.selectedProject}
            openCount={openCount}
            completedCount={completedCount}
            highCount={highCount}
            canManageMembers={props.projectRole === 'admin' || props.globalRole === 'admin'}
            onAddMembers={() => setAddMembersOpen(true)}
            addMembersButtonRef={addMembersTriggerRef}
          />
          {props.error && (
            <div className="error-banner" role="alert">
              {props.error}
              <button type="button" onClick={props.onDismissError}>Dismiss</button>
            </div>
          )}
          {props.moveToast && <div className="move-toast" role="alert"><span>{props.moveToast}</span><button type="button" aria-label="Dismiss move error" onClick={props.onDismissMoveToast}>×</button></div>}
          <BoardToolbar
            query={props.query}
            statusFilter={props.statusFilter}
            priorityFilter={props.priorityFilter}
            view={view}
            sortDescending={sortDescending}
            onQueryChange={props.onQueryChange}
            onStatusFilterChange={props.onStatusFilterChange}
            onPriorityFilterChange={props.onPriorityFilterChange}
            onViewChange={setView}
            onSortChange={() => setSortDescending(current => !current)}
          />
          {props.loading ? (
            <p className="state-message" role="status">Loading issues…</p>
          ) : (
            <>
              {sortedIssues.length === 0 && <p className="state-message">No issues match these filters.</p>}
              {view === 'board' ? (
                <>
                  {(props.query || props.statusFilter || props.priorityFilter) && <p className="drag-filter-note">Clear filters to reorder issues.</p>}
                  <KanbanBoard
                    issues={boardIssues(sortedIssues)}
                    onOpenIssue={props.onOpenIssue}
                    onSetStatus={props.onSetStatus}
                    onDeleteIssue={issue => requestDelete({ kind: 'issue', issue })}
                    currentUserID={props.currentUserID}
                    globalRole={props.globalRole}
                    onAddIssue={props.onAddIssue}
                    onMoveIssue={props.onMoveIssue}
                    disabled={props.movePending || Boolean(props.query || props.statusFilter || props.priorityFilter)}
                  />
                </>
              ) : sortedIssues.length > 0 ? (
                <IssueList issues={sortedIssues} onOpenIssue={props.onOpenIssue} onSetStatus={props.onSetStatus} onDeleteIssue={issue => requestDelete({ kind: 'issue', issue })} currentUserID={props.currentUserID} globalRole={props.globalRole} />
              ) : null}
            </>
          )}
          </> : props.screen === 'projects' ? <ProjectsDirectory projects={props.projects} adminProjects={props.adminProjects} adminProjectsLoading={props.adminProjectsLoading} adminProjectsError={props.adminProjectsError} isGlobalAdmin={props.globalRole === 'admin'} selectedProjectId={props.selectedProjectId} currentUserID={props.currentUserID} error={props.error} onDismissError={props.onDismissError} onSelectProject={id => { props.onSelectProject(id); props.onOpenBoard() }} onDeleteProject={project => setPendingDelete({ kind: 'project', project })} onLoadUserDirectory={props.onLoadUserDirectory} onCreateProject={props.onCreateProject} /> : props.screen === 'calendar' ? <ProjectCalendar project={props.selectedProject} issues={props.calendarIssues} loading={props.calendarLoading} onRangeChange={props.onCalendarRangeChange} onOpenIssue={props.onOpenIssue} /> : props.screen === 'admin' ? <ProjectAdminPanel members={props.members} role={props.projectRole} globalRole={props.globalRole} onSearchUsers={props.onSearchUsers} onAddMember={props.onAddMember} onRemoveMember={props.onRemoveMember} onSetMemberRole={props.onSetMemberRole} /> : props.screen === 'users' ? <UserManagementPage users={props.adminUsers} projects={props.adminProjects} loading={props.adminUsersLoading} error={props.adminUsersError} hasMore={props.adminUsersHasMore} managedUserID={props.managedUserID} managedProjectIDs={props.managedUserProjectIDs} managedProjectsLoading={props.managedUserProjectsLoading} managedProjectsError={props.managedUserProjectsError} onLoadMore={props.onLoadMoreUsers} onResetPassword={props.onResetUserPassword} onCreateUser={props.onCreateUser} onSetUserActive={props.onSetUserActive} onManageProjects={props.onManageUserProjects} onToggleProject={props.onToggleUserProject} currentUserID={props.currentUserID} /> : props.screen === 'my-issues' ? <MyIssuesPage issues={props.myIssues} projects={props.projects} loading={props.myIssuesLoading} error={props.myIssuesError} currentUserID={props.currentUserID} onOpenIssue={props.onOpenIssue} /> : props.screen === 'activity' ? <ActivityPage items={props.activityItems} projects={props.projects} loading={props.activityLoading} partialErrors={props.activityPartialErrors} /> : <AnalyticsPage project={props.selectedProject} snapshot={props.analytics} loading={props.analyticsLoading} error={props.analyticsError} />}
        </main>
      </div>
      {copilotOpen && <aside className="ai-copilot-panel" aria-label="AI Copilot">
        <ChatPage presentation="panel" onAsk={props.onAskTrackerAssistant} onClose={closeCopilot} />
      </aside>}
      {addMembersOpen && <AddMembersModal members={props.members} currentUserID={props.currentUserID} onLoadUserDirectory={props.onLoadUserDirectory} onAddMember={props.onAddMember} onClose={closeAddMembers} />}
      {pendingDelete && <DeleteConfirmationDialog
        title={pendingDelete.kind === 'project' ? `Delete project “${pendingDelete.project.name}”?` : 'Delete this issue?'}
        description={pendingDelete.kind === 'project'
          ? 'The project will be removed from active workspace views. Its issues and activity history will be retained.'
          : `Issue #${pendingDelete.issue.id} “${pendingDelete.issue.title}” will be permanently deleted. This action cannot be undone.`}
        confirmLabel={pendingDelete.kind === 'project' ? 'Delete project' : 'Delete issue'}
        onCancel={() => setPendingDelete(null)}
        onConfirm={confirmPendingDelete}
      />}
    </div>
  )
}

function DeleteConfirmationDialog({ title, description, confirmLabel, onCancel, onConfirm }: {
  title: string
  description: string
  confirmLabel: string
  onCancel: () => void
  onConfirm: () => Promise<void>
}) {
  const cancelButton = useRef<HTMLButtonElement>(null)
  const dialog = useRef<HTMLElement>(null)
  const returnFocus = useRef(document.activeElement instanceof HTMLElement ? document.activeElement : null)
  const [confirming, setConfirming] = useState(false)
  const confirmingRef = useRef(confirming)
  confirmingRef.current = confirming
  const [actionError, setActionError] = useState('')

  async function confirm() {
    setConfirming(true)
    setActionError('')
    try {
      await onConfirm()
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : 'The deletion could not be completed.')
      setConfirming(false)
    }
  }

  useEffect(() => {
    cancelButton.current?.focus()
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !confirmingRef.current) {
        event.preventDefault()
        onCancel()
      }
      if (event.key === 'Tab') {
        const items = dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])')
        if (!items?.length) {
          event.preventDefault()
          dialog.current?.focus()
          return
        }
        const first = items[0]
        const last = items[items.length - 1]
        if (event.shiftKey && (document.activeElement === first || !dialog.current?.contains(document.activeElement))) {
          event.preventDefault()
          last.focus()
        } else if (!event.shiftKey && (document.activeElement === last || !dialog.current?.contains(document.activeElement))) {
          event.preventDefault()
          first.focus()
        }
      }
    }
    window.addEventListener('keydown', onKeyDown, true)
    return () => {
      window.removeEventListener('keydown', onKeyDown, true)
      window.setTimeout(() => {
        if (returnFocus.current?.isConnected) {
          returnFocus.current.focus()
          return
        }
        const heading = document.querySelector<HTMLElement>('.main-area h1')
        if (heading) {
          heading.tabIndex = -1
          heading.focus()
        }
      }, 0)
    }
  }, [])

  return <div className="dialog-backdrop confirm-delete-backdrop" onClick={event => { if (event.target === event.currentTarget && !confirming) onCancel() }}>
    <section ref={dialog} className="issue-dialog confirm-delete-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-confirm-title" aria-describedby="delete-confirm-description" aria-busy={confirming} tabIndex={-1}>
      <div className="confirm-delete-icon"><span className="material-symbols-outlined" aria-hidden="true">delete</span></div>
      <p className="eyebrow">Confirm deletion</p>
      <h2 id="delete-confirm-title">{title}</h2>
      <p id="delete-confirm-description">{description}</p>
      {actionError && <p className="user-reset-error" role="alert">{actionError}</p>}
      <div className="dialog-actions">
        <button ref={cancelButton} type="button" className="secondary-button" disabled={confirming} onClick={onCancel}>Cancel</button>
        <button type="button" className="confirm-delete-button" disabled={confirming} onClick={() => void confirm()}>{confirming ? 'Deleting…' : confirmLabel}</button>
      </div>
    </section>
  </div>
}

function Sidebar({
  projects,
  selectedProjectId,
  screen,
  projectRole,
  globalRole,
  email,
  onSelectProject,
  onShowProjects,
  onShowCalendar,
  onShowAdmin,
  onShowMyIssues,
  onShowActivity,
  onShowAnalytics,
  onShowChat,
  onShowUsers,
  showUserManagement,
  onOpenBoard,
  onAddProject,
  onLogout,
}: Pick<ProjectBoardProps, 'projects' | 'selectedProjectId' | 'screen' | 'projectRole' | 'globalRole' | 'email' | 'onSelectProject' | 'onShowProjects' | 'onShowCalendar' | 'onShowAdmin' | 'onShowMyIssues' | 'onShowActivity' | 'onShowAnalytics' | 'onShowChat' | 'onShowUsers' | 'onOpenBoard' | 'onAddProject' | 'onLogout'> & { showUserManagement: boolean }) {
  return (
    <aside className="sidebar">
      <div className="sidebar-main">
        <a className="brand" href="#board" aria-label="Trackline home">
          <span className="brand-icon material-symbols-outlined" aria-hidden="true">view_kanban</span>
          <span className="brand-copy"><strong>Trackline</strong><small>Engineering Workspace</small></span>
        </a>
        <nav className="primary-nav" aria-label="Workspace">
          <button className={`nav-item${screen === 'projects' ? ' active' : ''}`} type="button" onClick={onShowProjects}><span className="material-symbols-outlined" aria-hidden="true">folder</span>Projects</button>
          <button className={`nav-item${screen === 'calendar' ? ' active' : ''}`} type="button" onClick={onShowCalendar}><span className="material-symbols-outlined" aria-hidden="true">calendar_month</span>Calendar</button>
          <button className={`nav-item${screen === 'admin' ? ' active' : ''}`} type="button" onClick={onShowAdmin}><span className="material-symbols-outlined" aria-hidden="true">admin_panel_settings</span>{projectRole === 'admin' ? 'Project admin' : 'Project team'}</button>
          <button className={`nav-item${screen === 'my-issues' ? ' active' : ''}`} type="button" onClick={onShowMyIssues}><span className="material-symbols-outlined" aria-hidden="true">checklist</span>My issues</button>
          <button className={`nav-item${screen === 'activity' ? ' active' : ''}`} type="button" onClick={onShowActivity}><span className="material-symbols-outlined" aria-hidden="true">switch_account</span>Activity</button>
          <button className={`nav-item${screen === 'analytics' ? ' active' : ''}`} type="button" onClick={onShowAnalytics}><span className="material-symbols-outlined" aria-hidden="true">monitoring</span>Analytics</button>
          <button className={`nav-item${screen === 'chat' ? ' active' : ''}`} type="button" onClick={onShowChat}><span className="material-symbols-outlined" aria-hidden="true">auto_awesome</span>AI Copilot</button>
          {showUserManagement && <button className={`nav-item${screen === 'users' ? ' active' : ''}`} type="button" onClick={onShowUsers}><span className="material-symbols-outlined" aria-hidden="true">manage_accounts</span>Users</button>}
        </nav>
        <div className="sidebar-projects">
          <div className="sidebar-section-title"><span>Projects</span><span className="material-symbols-outlined" aria-hidden="true">expand_more</span></div>
          <div className="project-list">
            {projects.map((project, index) => (
              <button
                className={`project-link${selectedProjectId === project.id ? ' selected' : ''}`}
                type="button"
                key={project.id}
                onClick={() => { onSelectProject(project.id); onOpenBoard() }}
              >
                <span className={`project-dot dot-${index % 4}`} />
                <span>{project.name}</span>
              </button>
            ))}
            {projects.length === 0 && <span className="sidebar-empty">No projects yet</span>}
          </div>
        </div>
      </div>
      <div className="sidebar-footer">
        <button className="new-project-button" type="button" onClick={onAddProject}>
          <span className="material-symbols-outlined" aria-hidden="true">add</span>New project
        </button>
        <div className="user-card">
          <span className="avatar">{email[0]?.toUpperCase() || 'U'}</span>
          <span className="user-copy"><strong>{email || 'Your workspace'}</strong><small>{globalRole === 'admin' ? 'Platform administrator' : 'Member account'} · {projectRole === 'admin' ? 'Project manager' : 'Project member'}</small></span>
          <button className="icon-button logout" type="button" aria-label="Sign out" title="Sign out" onClick={onLogout}>
            <span className="material-symbols-outlined" aria-hidden="true">logout</span>
          </button>
        </div>
      </div>
    </aside>
  )
}

function TopBar({ projectName, section, query, onQueryChange, onAddIssue, onOpenCopilot, copilotTriggerRef, onShowProjects, onOpenBoard, onOpenCurrentSection }: {
  projectName?: string
  section: string
  query: string
  onQueryChange: (value: string) => void
  onAddIssue: (status?: string) => void
  onOpenCopilot: () => void
  copilotTriggerRef: React.Ref<HTMLButtonElement>
  onShowProjects: () => void
  onOpenBoard: () => void
  onOpenCurrentSection: () => void
}) {
  return (
    <header className="topbar">
      <div className="topbar-left">
        <nav className="breadcrumbs" aria-label="Breadcrumb">
          <button className="breadcrumb-link" type="button" onClick={onShowProjects}>Projects</button><span className="breadcrumb-separator">/</span>
          <button className="breadcrumb-link" type="button" onClick={onOpenBoard}>{projectName || 'Your projects'}</button><span className="breadcrumb-separator">/</span>
          <button className="breadcrumb-current" type="button" aria-current="page" onClick={onOpenCurrentSection}>{section}</button>
        </nav>
        <label className="global-search">
          <span className="material-symbols-outlined" aria-hidden="true">search</span>
          <input aria-label="Global search" placeholder="Search issues, keys…" value={query} onChange={event => onQueryChange(event.target.value)} />
          <kbd>⌘K</kbd>
        </label>
      </div>
      <div className="topbar-actions">
        <button className="icon-button notification-button" type="button" aria-label="Notifications" title="Not available yet" disabled>
          <span className="material-symbols-outlined" aria-hidden="true">notifications</span><span className="notification-dot" />
        </button>
        <button className="icon-button" type="button" aria-label="Help" title="Not available yet" disabled><span className="material-symbols-outlined" aria-hidden="true">help</span></button>
        <span className="topbar-divider" />
        <button ref={copilotTriggerRef} className="secondary-button ask-ai-button" type="button" onClick={onOpenCopilot}><span className="material-symbols-outlined" aria-hidden="true">auto_awesome</span>Ask AI</button>
        <button className="primary-button" type="button" onClick={() => onAddIssue()}>
          <span className="material-symbols-outlined" aria-hidden="true">add</span>New issue
        </button>
        <span className="avatar avatar-small" aria-label="Your account">U</span>
      </div>
    </header>
  )
}

function ProjectHeader({ project, openCount, completedCount, highCount, canManageMembers, onAddMembers, addMembersButtonRef }: {
  project?: Project
  openCount: number
  completedCount: number
  highCount: number
  canManageMembers: boolean
  onAddMembers: () => void
  addMembersButtonRef: React.Ref<HTMLButtonElement>
}) {
  return (
    <section className="project-header" aria-labelledby="project-title">
      <div className="project-summary">
        <div className="project-title-row">
          <h1 id="project-title">{project?.name || 'Your projects'}</h1>
          {project && <span className="project-status">Project board</span>}
          {project && canManageMembers && <button ref={addMembersButtonRef} type="button" className="secondary-button board-add-members" onClick={onAddMembers}><span className="material-symbols-outlined" aria-hidden="true">group_add</span>Add members</button>}
        </div>
        <p>{project?.description || 'Create a project and start tracking what matters.'}</p>
      </div>
      <div className="project-stats" aria-label="Issue summary">
        <StatChip label="Open" value={openCount} tone="primary" />
        <StatChip label="Completed" value={completedCount} tone="success" />
        <StatChip label="High priority" value={highCount} tone="danger" />
      </div>
    </section>
  )
}

function AddMembersModal({ members, currentUserID, onLoadUserDirectory, onAddMember, onClose }: {
  members: ProjectMember[]
  currentUserID: number
  onLoadUserDirectory: (afterID: number | null) => Promise<UserDirectoryPage>
  onAddMember: (userID: number) => Promise<void>
  onClose: () => void
}) {
  const dialogRef = useRef<HTMLElement>(null)
  const [users, setUsers] = useState<UserSuggestion[]>([])
  const [selectedUsers, setSelectedUsers] = useState<UserSuggestion[]>([])
  const [filter, setFilter] = useState('')
  const [nextAfterID, setNextAfterID] = useState<number | null>(null)
  const [loading, setLoading] = useState(false)
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState('')
  const existingIDs = new Set(members.map(member => member.user_id))
  const pickerUsersByID = new Map(users.filter(user => user.id !== currentUserID).map(user => [user.id, user]))
  for (const member of members) {
    if (member.user_id > 0 && member.user_id !== currentUserID && !pickerUsersByID.has(member.user_id)) {
      pickerUsersByID.set(member.user_id, { id: member.user_id, username: member.username || `User #${member.user_id}` })
    }
  }
  const visibleUsers = Array.from(pickerUsersByID.values()).filter(user => user.username.toLowerCase().includes(filter.toLowerCase()))

  async function loadUsers(afterID: number | null) {
    setLoading(true)
    setError('')
    try {
      const page = await onLoadUserDirectory(afterID)
      const available = page.users.filter(user => user.id !== currentUserID)
      setUsers(current => afterID === null ? available : [...current, ...available])
      setNextAfterID(page.next_after_id)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load users.')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadUsers(null)
    dialogRef.current?.querySelector<HTMLElement>('button:not([disabled]), input:not([disabled])')?.focus()
  }, [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !adding) {
        onClose()
        return
      }
      if (event.key !== 'Tab' || !dialogRef.current) return
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])'))
      if (!focusable.length) {
        event.preventDefault()
        dialogRef.current.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current.contains(document.activeElement))) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (document.activeElement === last || !dialogRef.current.contains(document.activeElement))) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [adding, onClose])

  function toggleUser(user: UserSuggestion, checked: boolean) {
    setSelectedUsers(current => checked
      ? current.some(item => item.id === user.id) ? current : [...current, user]
      : current.filter(item => item.id !== user.id))
  }

  async function addSelected() {
    if (!selectedUsers.length) return
    setAdding(true)
    setError('')
    const addedIDs: number[] = []
    const failures: string[] = []
    for (const user of selectedUsers) {
      try {
        await onAddMember(user.id)
        addedIDs.push(user.id)
      } catch {
        failures.push(user.username)
      }
    }
    setAdding(false)
    setSelectedUsers(current => current.filter(user => !addedIDs.includes(user.id)))
    if (failures.length) {
      setError(`${addedIDs.length ? `Added ${addedIDs.length} member(s). ` : ''}Could not add: ${failures.join(', ')}. Please retry.`)
    } else {
      onClose()
    }
  }

  return <div className="dialog-backdrop add-members-backdrop">
    <section ref={dialogRef} className="issue-dialog add-members-dialog" role="dialog" aria-modal="true" aria-labelledby="add-members-title" tabIndex={-1}>
      <div className="dialog-heading"><div><p className="eyebrow">Project access</p><h2 id="add-members-title">Add members</h2></div><button type="button" aria-label="Close add members" onClick={onClose} disabled={adding}>×</button></div>
      <p className="add-members-description">Select active users to add to this project. They will be added as members.</p>
      <input className="project-member-filter" aria-label="Filter users to add" value={filter} onChange={event => setFilter(event.target.value)} placeholder="Filter users…" />
      {error && <p className="error-banner" role="alert">{error}</p>}
      {loading && users.length === 0 ? <p className="state-message" role="status">Loading users…</p> : visibleUsers.length ? <div className="project-user-directory add-members-user-list" role="group" aria-label="Available users">{visibleUsers.map(user => {
        const existing = existingIDs.has(user.id)
        const selected = selectedUsers.some(item => item.id === user.id)
        return <button key={user.id} type="button" className={`member-picker-card${selected ? ' selected' : ''}${existing ? ' existing' : ''}`} aria-label={existing ? `${user.username}, already a member` : `${user.username}, ${selected ? 'selected' : 'not selected'}`} aria-pressed={selected || existing} disabled={adding || existing} onClick={() => toggleUser(user, !selected)}>
          <span className="member-picker-card-top"><span className="member-avatar">{user.username[0]?.toUpperCase() || 'U'}</span><span className={`material-symbols-outlined member-picker-indicator`} aria-hidden="true">{existing ? 'check_circle' : selected ? 'check_circle' : 'radio_button_unchecked'}</span></span>
          <span className="project-user-identity"><strong>{user.username}</strong><small>User #{user.id}</small></span>
          {existing && <span className="member-picker-status">Already a member</span>}
        </button>
      })}</div> : !error && !loading ? <p className="state-message">{filter ? 'No users match this filter.' : 'No active users available to add.'}</p> : null}
      <div className="project-member-list-footer"><span>{selectedUsers.length} selected</span>{nextAfterID !== null && <button type="button" className="secondary-button" disabled={loading || adding} onClick={() => void loadUsers(nextAfterID)}>{loading ? 'Loading…' : 'Load more users'}</button>}</div>
      <div className="dialog-actions"><button type="button" className="secondary-button" onClick={onClose} disabled={adding}>Cancel</button><button type="button" className="primary-button" onClick={() => void addSelected()} disabled={adding || selectedUsers.length === 0}>{adding ? 'Adding…' : `Add ${selectedUsers.length || ''} member${selectedUsers.length === 1 ? '' : 's'}`}</button></div>
    </section>
  </div>
}

function ProjectsDirectory({ projects, adminProjects, adminProjectsLoading, adminProjectsError, isGlobalAdmin, selectedProjectId, currentUserID, error, onDismissError, onSelectProject, onDeleteProject, onLoadUserDirectory, onCreateProject }: {
  projects: Project[]
  adminProjects: Project[]
  adminProjectsLoading: boolean
  adminProjectsError: string
  isGlobalAdmin: boolean
  selectedProjectId: number | null
  currentUserID: number
  error: string
  onDismissError: () => void
  onSelectProject: (id: number) => void
  onDeleteProject: (project: Project) => void
  onLoadUserDirectory: (afterID: number | null) => Promise<UserDirectoryPage>
  onCreateProject: (event: React.FormEvent<HTMLFormElement>, memberIDs: number[]) => Promise<boolean>
}) {
  const [query, setQuery] = useState('')
  const [directoryUsers, setDirectoryUsers] = useState<UserSuggestion[]>([])
  const [memberFilter, setMemberFilter] = useState('')
  const [selectedMembers, setSelectedMembers] = useState<UserSuggestion[]>([])
  const [directoryError, setDirectoryError] = useState('')
  const [directoryLoading, setDirectoryLoading] = useState(false)
  const [directoryNextAfterID, setDirectoryNextAfterID] = useState<number | null>(null)
  const [creating, setCreating] = useState(false)
  const filtered = projects.filter(project => `${project.name} ${project.description}`.toLowerCase().includes(query.toLowerCase()))
  const filteredAdminProjects = adminProjects.filter(project => `${project.name} ${project.description} ${project.owner_username || ''} ${project.owner_id || ''}`.toLowerCase().includes(query.toLowerCase()))
  const filteredUsers = directoryUsers.filter(user => user.username.toLowerCase().includes(memberFilter.toLowerCase()))

  async function loadUserDirectory(afterID: number | null) {
    setDirectoryLoading(true)
    setDirectoryError('')
    try {
      const page = await onLoadUserDirectory(afterID)
      const availableUsers = page.users.filter(user => user.id !== currentUserID)
      setDirectoryUsers(current => afterID === null ? availableUsers : [...current, ...availableUsers])
      setDirectoryNextAfterID(page.next_after_id)
    } catch (cause) {
      setDirectoryError(cause instanceof Error ? cause.message : 'Could not load users.')
    } finally {
      setDirectoryLoading(false)
    }
  }

  useEffect(() => { void loadUserDirectory(null) }, [])

  function toggleSelectedMember(user: UserSuggestion, selected: boolean) {
    setSelectedMembers(current => {
      if (!selected) return current.filter(member => member.id !== user.id)
      if (current.length >= 50 || current.some(member => member.id === user.id)) return current
      return [...current, user]
    })
  }

  async function submitProject(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    setCreating(true)
    try {
      if (await onCreateProject(event, selectedMembers.map(user => user.id))) {
        form.reset()
        setSelectedMembers([])
      }
    } finally {
      setCreating(false)
    }
  }
  return <section className="directory-page" aria-labelledby="directory-title">
    <div className="directory-heading">
      <div><p className="eyebrow">Workspace / Projects</p><h1 id="directory-title">Projects Directory</h1><p>{isGlobalAdmin ? 'All projects across the workspace, with access to every project board.' : 'All your team’s work, organized in one place.'}</p></div>
    </div>
    {error && <div className="error-banner" role="alert">{error}<button type="button" onClick={onDismissError}>Dismiss</button></div>}
    {isGlobalAdmin && <div className="directory-toolbar admin-project-toolbar"><label className="directory-search"><span className="material-symbols-outlined" aria-hidden="true">search</span><input aria-label="Search projects" placeholder="Search by project, description, creator, or ID…" value={query} onChange={event => setQuery(event.target.value)} />{query && <button type="button" className="clear-project-search" aria-label="Clear project search" onClick={() => setQuery('')}>×</button>}</label><span aria-live="polite">{query ? `${filteredAdminProjects.length} of ${adminProjects.length} projects` : `${adminProjects.length} ${adminProjects.length === 1 ? 'project' : 'projects'}`}</span></div>}
    {isGlobalAdmin && <section className="platform-projects-section" aria-labelledby="platform-projects-title">
      <div className="platform-projects-heading"><div><p className="eyebrow">Platform administration</p><h2 id="platform-projects-title">All user projects</h2><p>Review project owners and open any project board.</p></div></div>
      {adminProjectsError && <p className="error-banner" role="alert">{adminProjectsError}</p>}
      {adminProjectsLoading && adminProjects.length === 0 ? <p className="state-message" role="status">Loading all projects…</p> : filteredAdminProjects.length ? <div className="platform-project-list">{filteredAdminProjects.map(project => {
        const canOpen = isGlobalAdmin || projects.some(memberProject => memberProject.id === project.id)
        return <article className="platform-project-card" key={project.id}>
          <div className="platform-project-card-heading">
            <span className="directory-project-icon"><span className="material-symbols-outlined" aria-hidden="true">folder_open</span></span>
            <span className="platform-project-id">#{project.id}</span>
          </div>
          <div className="platform-project-copy"><h3>{project.name}</h3><p>{project.description || 'No project description yet.'}</p></div>
          <div className="platform-project-owner"><span>CREATED BY</span><strong>{project.owner_username || `User #${project.owner_id ?? 'unknown'}`}</strong></div>
          <div className="project-card-actions">
            {canOpen ? <button type="button" className={`directory-open${selectedProjectId === project.id ? ' current' : ''}`} onClick={() => onSelectProject(project.id)}>{selectedProjectId === project.id ? 'Open current board' : 'Open board'}<span className="material-symbols-outlined" aria-hidden="true">arrow_forward</span></button> : <span className="platform-project-access" title="Join this project before opening its board">Not a member</span>}
            {(isGlobalAdmin || project.owner_id === currentUserID) && <button type="button" className="project-delete-button" aria-label={`Delete project ${project.name}`} onMouseDown={event => event.currentTarget.focus()} onClick={() => onDeleteProject(project)}><span className="material-symbols-outlined" aria-hidden="true">delete</span>Delete</button>}
          </div>
        </article>
      })}</div> : !adminProjectsError && <p className="state-message">{adminProjects.length ? 'No projects match this search.' : 'No projects have been created yet.'}</p>}
    </section>}
    <section className="creation-panel directory-create-project">
      <h2>Create a project</h2>
      <form onSubmit={event => void submitProject(event)}><div className="creation-fields"><input id="project-name" name="name" placeholder="Project name" required /><input name="description" placeholder="Short description" /><button className="primary-button" type="submit" disabled={creating}>{creating ? 'Creating…' : 'Create project'}</button></div></form>
      <div className="project-invite-picker"><strong>Choose project members</strong><p>Select up to 50 active users to add as project members. You will be the project manager.</p>
        <input className="project-member-filter" aria-label="Filter listed users" value={memberFilter} onChange={event => setMemberFilter(event.target.value)} placeholder="Filter listed users…" />
        {directoryError && <p className="user-reset-error" role="alert">{directoryError}<button type="button" className="text-button" onClick={() => void loadUserDirectory(null)}>Retry</button></p>}
        {directoryLoading && directoryUsers.length === 0 ? <p className="state-message" role="status">Loading users…</p> : filteredUsers.length ? <div className="project-user-directory" role="group" aria-label="Select project members">{filteredUsers.map(user => { const checked = selectedMembers.some(member => member.id === user.id); return <label className="project-user-option" key={user.id}><input type="checkbox" checked={checked} disabled={!checked && selectedMembers.length >= 50} onChange={event => toggleSelectedMember(user, event.target.checked)} /><span className="member-avatar">{user.username[0]?.toUpperCase() || 'U'}</span><span className="project-user-identity"><strong>{user.username}</strong><small>User #{user.id}</small></span></label> })}</div> : !directoryError && !directoryLoading ? <p className="state-message">{memberFilter ? 'No listed users match this filter.' : 'No other active users to add.'}</p> : null}
        <div className="project-member-list-footer"><span>{selectedMembers.length} selected</span>{directoryNextAfterID !== null && <button type="button" className="secondary-button" disabled={directoryLoading} onClick={() => void loadUserDirectory(directoryNextAfterID)}>{directoryLoading ? 'Loading…' : 'Load more users'}</button>}</div>
      </div>
    </section>
    {!isGlobalAdmin && <div className="directory-toolbar"><label className="directory-search"><span className="material-symbols-outlined" aria-hidden="true">search</span><input aria-label="Search projects" placeholder="Search projects..." value={query} onChange={event => setQuery(event.target.value)} /></label><span>{filtered.length} {filtered.length === 1 ? 'project' : 'projects'}</span></div>}
    {!isGlobalAdmin && filtered.length ? <div className="project-directory-grid">{filtered.map((project, index) => <article className="directory-card" key={project.id}>
      <div className="directory-card-top"><span className={`directory-project-icon dot-${index % 4}`}><span className="material-symbols-outlined" aria-hidden="true">folder_open</span></span><span className="project-status">Project</span></div>
      <h2>{project.name}</h2><p>{project.description || 'No project description yet.'}</p>
      <div className="directory-card-meta"><span><span className="material-symbols-outlined" aria-hidden="true">confirmation_number</span>Issues available in board</span><span>#{project.id}</span></div>
      <div className="project-card-actions">
        <button type="button" className={`directory-open${selectedProjectId === project.id ? ' current' : ''}`} onClick={() => onSelectProject(project.id)}>{selectedProjectId === project.id ? 'Open current board' : 'Open project'}<span className="material-symbols-outlined" aria-hidden="true">arrow_forward</span></button>
        {project.owner_id === currentUserID && <button type="button" className="project-delete-button" aria-label={`Delete project ${project.name}`} onMouseDown={event => event.currentTarget.focus()} onClick={() => onDeleteProject(project)}><span className="material-symbols-outlined" aria-hidden="true">delete</span>Delete</button>}
      </div>
    </article>)}</div> : !isGlobalAdmin && <div className="directory-empty"><span className="material-symbols-outlined" aria-hidden="true">folder_off</span><h2>{projects.length ? 'No matching projects' : 'No projects yet'}</h2><p>{projects.length ? 'Try another search.' : 'Create a project using the form above to give your team a place to track issues.'}</p></div>}
  </section>
}

function UserManagementPage({ users, projects, loading, error, hasMore, managedUserID, managedProjectIDs, managedProjectsLoading, managedProjectsError, onLoadMore, onResetPassword, onCreateUser, onSetUserActive, onManageProjects, onToggleProject, currentUserID }: {
  users: AdminUser[]
  projects: Project[]
  loading: boolean
  error: string
  hasMore: boolean
  managedUserID: number | null
  managedProjectIDs: number[]
  managedProjectsLoading: boolean
  managedProjectsError: string
  onLoadMore: () => void
  onResetPassword: (userID: number, temporaryPassword: string) => Promise<void>
  onCreateUser: (email: string, temporaryPassword: string) => Promise<void>
  onSetUserActive: (userID: number, active: boolean) => Promise<void>
  onManageProjects: (userID: number) => void
  onToggleProject: (userID: number, projectID: number, add: boolean) => Promise<void>
  currentUserID: number
}) {
  const [passwords, setPasswords] = useState<Record<number, string>>({})
  const [busyID, setBusyID] = useState<number | null>(null)
  const [messages, setMessages] = useState<Record<number, string>>({})
  const [newEmail, setNewEmail] = useState('')
  const [newTemporaryPassword, setNewTemporaryPassword] = useState('')
  const [creatingUser, setCreatingUser] = useState(false)
  const [createError, setCreateError] = useState('')
  const [projectError, setProjectError] = useState('')
  const [projectBusyID, setProjectBusyID] = useState<number | null>(null)

  async function reset(userID: number) {
    const password = passwords[userID] || ''
    if (password.length < 8) return
    setBusyID(userID)
    setMessages(current => ({ ...current, [userID]: '' }))
    try {
      await onResetPassword(userID, password)
      setPasswords(current => ({ ...current, [userID]: '' }))
      setMessages(current => ({ ...current, [userID]: 'Temporary password set. User must change it at next login.' }))
    } catch (cause) {
      setMessages(current => ({ ...current, [userID]: cause instanceof Error ? cause.message : 'Could not reset password.' }))
    } finally {
      setBusyID(null)
    }
  }

  async function createUser(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setCreatingUser(true)
    setCreateError('')
    try {
      await onCreateUser(newEmail, newTemporaryPassword)
      setNewEmail('')
      setNewTemporaryPassword('')
      setCreateError('Account created. Share the temporary password securely; user must change it at first sign-in.')
    } catch (cause) {
      setCreateError(cause instanceof Error ? cause.message : 'Could not create account.')
    } finally {
      setCreatingUser(false)
    }
  }

  async function setActive(user: AdminUser) {
    setBusyID(user.id)
    setMessages(current => ({ ...current, [user.id]: '' }))
    try {
      await onSetUserActive(user.id, !user.active)
      setMessages(current => ({ ...current, [user.id]: user.active ? 'Account deactivated. Project memberships and issue history are retained.' : 'Account reactivated.' }))
    } catch (cause) {
      setMessages(current => ({ ...current, [user.id]: cause instanceof Error ? cause.message : 'Could not update account.' }))
    } finally {
      setBusyID(null)
    }
  }

  async function toggleProject(userID: number, projectID: number, add: boolean) {
    setProjectBusyID(projectID)
    setProjectError('')
    try {
      await onToggleProject(userID, projectID, add)
    } catch (cause) {
      setProjectError(cause instanceof Error ? cause.message : 'Could not update project membership.')
    } finally {
      setProjectBusyID(null)
    }
  }

  return <section className="workspace-page user-management-page" aria-labelledby="users-title">
    <div className="directory-heading"><div><p className="eyebrow">Platform administration</p><h1 id="users-title">Users & projects</h1><p>Create accounts, control access, and assign people to projects.</p></div><span className="role-badge admin">Global admin</span></div>
    <p className="admin-readonly-note">Account removal deactivates sign-in rather than deleting history. Project memberships and issue attribution remain intact. Passwords are never viewable; temporary passwords must be changed at first sign-in.</p>
    {error && <p className="error-banner" role="alert">{error}</p>}
    <section className="admin-card user-create-card"><div><h2>Add a user</h2><p>New accounts start as members and must change the temporary password on first sign-in.</p></div><form className="user-create-form" onSubmit={event => void createUser(event)}><label>Email<input type="email" value={newEmail} onChange={event => setNewEmail(event.target.value)} required /></label><label>Temporary password<input type="password" autoComplete="new-password" minLength={8} maxLength={72} value={newTemporaryPassword} onChange={event => setNewTemporaryPassword(event.target.value)} required /></label><button className="primary-button" type="submit" disabled={creatingUser}>{creatingUser ? 'Creating…' : 'Create user'}</button></form>{createError && <p className={createError.startsWith('Account created') ? 'user-reset-success' : 'user-reset-error'} role="status">{createError}</p>}</section>
    {loading && users.length === 0 ? <p className="state-message" role="status">Loading users…</p> : users.length === 0 ? <div className="directory-empty"><span className="material-symbols-outlined" aria-hidden="true">group_off</span><h2>No users found</h2></div> : <div className="user-management-list">{users.map(user => <article className="user-management-row" key={user.id}>
      <span className="member-avatar">{user.email[0]?.toUpperCase() || 'U'}</span><div className="user-management-info"><strong>{user.email}</strong><span>User #{user.id} · {user.role === 'admin' ? 'Platform administrator' : 'Member'} · Joined {new Date(user.created_at).toLocaleDateString()}</span><em className={user.active ? 'account-active' : 'account-inactive'}>{user.active ? 'Active' : 'Deactivated'}</em>{user.password_reset_required && <em>Password change required</em>}</div>
      <div className="user-management-actions"><button type="button" className="secondary-button" onClick={() => { setProjectError(''); onManageProjects(user.id) }}>{managedUserID === user.id ? 'Refresh projects' : 'Manage projects'}</button><button type="button" className="secondary-button" onClick={() => void setActive(user)} disabled={busyID === user.id || user.id === currentUserID}>{busyID === user.id ? 'Updating…' : user.active ? 'Deactivate' : 'Reactivate'}</button></div>
      <form className="user-reset-form" onSubmit={event => { event.preventDefault(); void reset(user.id) }}><label className="visually-hidden" htmlFor={`temporary-password-${user.id}`}>Temporary password for {user.email}</label><input id={`temporary-password-${user.id}`} type="password" autoComplete="new-password" minLength={8} maxLength={72} required placeholder="Set temporary password" value={passwords[user.id] || ''} onChange={event => setPasswords(current => ({ ...current, [user.id]: event.target.value }))} /><button className="secondary-button" type="submit" disabled={busyID === user.id}>{busyID === user.id ? 'Resetting…' : 'Reset password'}</button></form>
      {messages[user.id] && <p className={messages[user.id].includes('Account') || messages[user.id].includes('deactivated') || messages[user.id].includes('reactivated') ? 'user-reset-success' : 'user-reset-error'} role="status">{messages[user.id]}</p>}
      {managedUserID === user.id && <div className="user-project-access"><div><h3>Project access</h3><p>{user.active ? 'Select projects where this user should be a member.' : 'This account is inactive. Reactivate it before adding project memberships.'}</p></div>{projectError && <p className="user-reset-error" role="alert">{projectError}</p>}{managedProjectsLoading ? <p role="status">Loading project access…</p> : managedProjectsError ? <p className="user-reset-error" role="alert">{managedProjectsError}</p> : projects.length === 0 ? <p>No projects exist yet.</p> : <div className="user-project-list">{projects.map(project => { const globalAdminAccess = user.id === currentUserID && user.role === 'admin'; const member = managedProjectIDs.includes(project.id) || globalAdminAccess; const owner = project.owner_id === user.id; return <label key={project.id}><input type="checkbox" checked={member} disabled={globalAdminAccess || projectBusyID === project.id || (!member && !user.active) || (member && owner)} onChange={event => void toggleProject(user.id, project.id, event.target.checked)} /><span>{project.name}{globalAdminAccess ? ' · Global admin access' : owner ? ' · Project owner' : ''}</span>{projectBusyID === project.id && <small>Updating…</small>}</label> })}</div>}</div>}
    </article>)}</div>}
    {loading && users.length > 0 && <p className="state-message" role="status">Loading more users…</p>}
    {hasMore && <button type="button" className="secondary-button load-more-users" disabled={loading} onClick={onLoadMore}>Load more users</button>}
  </section>
}

function ProjectAdminPanel({ members, role, globalRole, onSearchUsers, onAddMember, onRemoveMember, onSetMemberRole }: {
  members: ProjectMember[]
  role: string
  globalRole: string
  onSearchUsers: (prefix: string) => Promise<UserSuggestion[]>
  onAddMember: (userID: number) => Promise<void>
  onRemoveMember: (userID: number) => Promise<void>
  onSetMemberRole: (userID: number, role: 'admin' | 'member') => Promise<void>
}) {
  const [query, setQuery] = useState('')
  const [suggestions, setSuggestions] = useState<UserSuggestion[]>([])
  const [error, setError] = useState('')
  const [busyUser, setBusyUser] = useState<number | null>(null)
  const isAdmin = role === 'admin' || globalRole === 'admin'

  async function search(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!query.trim()) return
    setError('')
    try { setSuggestions(await onSearchUsers(query.trim())) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not find users') }
  }

  async function add(userID: number) {
    setBusyUser(userID)
    setError('')
    try { await onAddMember(userID); setSuggestions(current => current.filter(user => user.id !== userID)) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not add member') } finally { setBusyUser(null) }
  }

  async function updateRole(userID: number, nextRole: 'admin' | 'member') {
    setBusyUser(userID)
    setError('')
    try { await onSetMemberRole(userID, nextRole) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not update role') } finally { setBusyUser(null) }
  }

  async function remove(userID: number) {
    setBusyUser(userID)
    setError('')
    try { await onRemoveMember(userID) } catch (cause) { setError(cause instanceof Error ? cause.message : 'Could not remove member') } finally { setBusyUser(null) }
  }

  return <section className="project-admin-page" aria-labelledby="project-admin-title">
    <div className="directory-heading"><div><p className="eyebrow">Workspace / Project access</p><h1 id="project-admin-title">Project team & roles</h1><p>Project managers and global admins control membership and who can be assigned work.</p></div><span className={`role-badge ${isAdmin ? 'admin' : ''}`}>{globalRole === 'admin' ? 'Global admin' : isAdmin ? 'Project manager' : 'Project member'}</span></div>
    {!isAdmin && <p className="admin-readonly-note">Only project managers can add or remove members and change roles.</p>}
    {isAdmin && <section className="admin-card"><div><h2>Add a project member</h2><p>Search by username. New users are added as project members.</p></div><form className="member-search-form" onSubmit={search}><input aria-label="Find users by username" value={query} onChange={event => setQuery(event.target.value)} placeholder="Start typing a username..."/><button className="primary-button" type="submit">Search users</button></form>
      {suggestions.length > 0 && <ul className="user-suggestion-list">{suggestions.map(user => <li key={user.id}><span><strong>{user.username}</strong><small>User #{user.id}</small></span><button type="button" className="secondary-button" disabled={busyUser === user.id || members.some(member => member.user_id === user.id)} onClick={() => void add(user.id)}>{members.some(member => member.user_id === user.id) ? 'Already a member' : busyUser === user.id ? 'Adding…' : 'Add member'}</button></li>)}</ul>}
    </section>}
    {error && <p role="alert" className="error-banner">{error}</p>}
    <section className="admin-card"><div className="admin-card-heading"><div><h2>Members</h2><p>{members.length} {members.length === 1 ? 'person' : 'people'} in this project</p></div></div>
      {members.length ? <div className="member-table">{members.map(member => <div className={`member-row${member.active === false ? ' inactive-member' : ''}`} key={member.user_id}><span className="member-avatar">{member.username?.[0]?.toUpperCase() || String(member.user_id).slice(-2)}</span><span className="member-identity"><strong>{member.username || `User #${member.user_id}`}</strong><small>{member.synthetic ? 'Global admin with default project access' : member.active === false ? 'Deactivated account' : member.user_id === 0 ? 'Unknown' : member.role === 'admin' ? 'Can manage the project' : 'Can work on assigned issues'}</small></span><span className={`role-badge ${member.role === 'admin' || member.synthetic ? 'admin' : ''}`}>{member.synthetic ? 'Platform admin' : member.role === 'admin' ? 'Project manager' : 'Member'}</span>
        {isAdmin && member.user_id !== 0 && !member.synthetic && <div className="member-actions"><button type="button" className="secondary-button" disabled={busyUser === member.user_id} onClick={() => void updateRole(member.user_id, member.role === 'admin' ? 'member' : 'admin')}>{member.role === 'admin' ? 'Make member' : 'Make manager'}</button><button type="button" className="danger-button" disabled={busyUser === member.user_id} onClick={() => void remove(member.user_id)}>Remove</button></div>}
      </div>)}</div> : <p className="admin-readonly-note">No members are listed.</p>}
    </section>
  </section>
}

function MyIssuesPage({ issues, projects, loading, error, currentUserID, onOpenIssue }: {
  issues: Issue[]
  projects: Project[]
  loading: boolean
  error: string
  currentUserID: number
  onOpenIssue: (issue: Issue) => void
}) {
  const [includeCompleted, setIncludeCompleted] = useState(false)
  const visibleIssues = includeCompleted ? issues : issues.filter(issue => issue.status !== 'done')
  const projectNames = new Map(projects.map(project => [project.id, project.name]))
  return <section className="workspace-page" aria-labelledby="my-issues-title">
    <div className="directory-heading"><div><p className="eyebrow">Workspace / Personal</p><h1 id="my-issues-title">My issues</h1><p>Issues assigned to you across your project teams.</p></div><label className="completed-toggle"><input type="checkbox" checked={includeCompleted} onChange={event => setIncludeCompleted(event.target.checked)} />Include completed</label></div>
    {loading ? <p className="state-message" role="status">Loading your issues…</p> : visibleIssues.length ? <div className="workspace-issue-list">{visibleIssues.map(issue => <article className="workspace-issue-row" key={issue.id}>
      <span className="issue-id">#{issue.id}</span><div className="workspace-issue-main"><button type="button" className="issue-title" onClick={() => onOpenIssue(issue)}>{issue.title}</button><span>{projectNames.get(issue.project_id || 0) || `Project #${issue.project_id}`}</span></div>
      <span className={`priority-badge ${issue.priority}`}>{capitalize(issue.priority)}</span><span className={`status-pill ${issue.status === 'in_progress' ? 'progress' : issue.status}`}>{statusLabel(issue.status)}</span>
      {issue.due_date && <time className="workspace-due-date">Due {issue.due_date}</time>}
    </article>)}</div> : error ? <p className="error-banner" role="alert">Could not load your issues: {error}</p> : <div className="directory-empty"><span className="material-symbols-outlined" aria-hidden="true">task_alt</span><h2>{currentUserID ? 'No assigned issues' : 'Sign in to view your issues'}</h2><p>{includeCompleted ? 'Issues assigned to you will appear here.' : 'You have no open issues assigned to you.'}</p></div>}
    {!loading && issues.length > 0 && <p className="workspace-footnote">Showing {visibleIssues.length} of {issues.length} assigned issues.</p>}
  </section>
}

function ActivityPage({ items, projects, loading, partialErrors }: {
  items: ActivityEntry[]
  projects: Project[]
  loading: boolean
  partialErrors: string[]
}) {
  const projectNames = new Map(projects.map(project => [project.id, project.name]))
  return <section className="workspace-page" aria-labelledby="activity-title">
    <div className="directory-heading"><div><p className="eyebrow">Workspace / Updates</p><h1 id="activity-title">Activity</h1><p>Recent issue events from the projects you belong to.</p></div><span className="project-status">Latest 100 per project</span></div>
    {loading && <p className="state-message" role="status">Loading recent activity…</p>}
    {partialErrors.length > 0 && <p className="admin-readonly-note" role="status">Could not load activity for {partialErrors.join(', ')}. Other project activity is shown where available.</p>}
    {!loading && items.length === 0 ? <div className="directory-empty"><span className="material-symbols-outlined" aria-hidden="true">{partialErrors.length === projects.length && projects.length ? 'cloud_off' : 'history'}</span><h2>{partialErrors.length === projects.length && projects.length ? 'Activity unavailable' : 'No activity yet'}</h2><p>{partialErrors.length === projects.length && projects.length ? 'Could not load activity from your projects. Try opening Activity again.' : 'Issue creation and status updates will appear here.'}</p></div> : !loading && <div className="activity-feed">{items.map(item => <article className="activity-feed-item" key={`${item.project_id}-${item.id}`}><span className="activity-feed-icon material-symbols-outlined" aria-hidden="true">{item.event_type === 'IssueCreated' ? 'add_task' : 'sync_alt'}</span><div className="activity-feed-copy"><strong>{feedDescription(item)}</strong><span>{projectNames.get(item.project_id) || `Project #${item.project_id}`} · Issue #{item.issue_id} · User #{item.actor_id}</span></div><time dateTime={item.occurred_at}>{new Date(item.occurred_at).toLocaleString()}</time></article>)}</div>}
    {!loading && items.length > 0 && <p className="workspace-footnote">Recent feed only; up to 100 events are loaded per project.</p>}
  </section>
}

function AnalyticsPage({ project, snapshot, loading, error }: {
  project?: Project
  snapshot: AnalyticsSnapshot | null
  loading: boolean
  error: string
}) {
  return <section className="workspace-page" aria-labelledby="analytics-title">
    <div className="directory-heading"><div><p className="eyebrow">Workspace / Project snapshot</p><h1 id="analytics-title">Analytics</h1><p>Current issue distribution{project ? ` for ${project.name}` : ''}. Historical trends are not available yet.</p></div>{project && <span className="project-status">Project #{project.id}</span>}</div>
    {!project && <div className="directory-empty"><span className="material-symbols-outlined" aria-hidden="true">folder_off</span><h2>Select a project</h2><p>Choose a project from the sidebar to view its analytics.</p></div>}
    {loading && <p className="state-message" role="status">Loading project analytics…</p>}
    {error && <p className="error-banner" role="alert">Could not load analytics: {error}</p>}
    {!loading && project && snapshot && <>
      <div className="analytics-summary-grid"><article className="analytics-summary-card"><span>Total issues</span><strong>{snapshot.total}</strong><small>Current project total</small></article><article className="analytics-summary-card"><span>Open high priority</span><strong>{snapshot.open_high_priority}</strong><small>High and critical, not done</small></article><article className="analytics-summary-card"><span>In progress</span><strong>{snapshot.by_status.in_progress || 0}</strong><small>Currently being worked</small></article><article className="analytics-summary-card"><span>Completed</span><strong>{snapshot.by_status.done || 0}</strong><small>Current completed issues</small></article></div>
      <div className="analytics-breakdowns"><section className="admin-card"><h2>Issues by status</h2>{[['To do', snapshot.by_status.todo || 0, 'todo'], ['In progress', snapshot.by_status.in_progress || 0, 'progress'], ['Done', snapshot.by_status.done || 0, 'done']].map(([label, count, tone]) => <MetricBar key={String(label)} label={String(label)} count={Number(count)} total={snapshot.total} tone={String(tone)} />)}</section><section className="admin-card"><h2>Issues by priority</h2>{[['Critical', snapshot.by_priority.critical || 0, 'critical'], ['High', snapshot.by_priority.high || 0, 'high'], ['Medium', snapshot.by_priority.medium || 0, 'medium'], ['Low', snapshot.by_priority.low || 0, 'low']].map(([label, count, tone]) => <MetricBar key={String(label)} label={String(label)} count={Number(count)} total={snapshot.total} tone={String(tone)} />)}</section></div>
      <p className="workspace-footnote">Counts reflect current issue state, not a historical trend or velocity report.</p>
    </>}
  </section>
}

function MetricBar({ label, count, total, tone }: { label: string; count: number; total: number; tone: string }) {
  const percent = total ? Math.round(count / total * 100) : 0
  return <div className="metric-bar-row"><div><span>{label}</span><strong>{count}</strong></div><div className="metric-bar-track"><i className={`metric-bar-fill ${tone}`} style={{ width: `${percent}%` }} /></div></div>
}

function screenLabel(screen: ProjectBoardProps['screen']) {
  return ({ board: 'Board', projects: 'Projects', calendar: 'Calendar', admin: 'Project team', 'my-issues': 'My issues', activity: 'Activity', analytics: 'Analytics', users: 'Users', chat: 'AI Copilot' })[screen]
}

function statusLabel(status: string) {
  return ({ todo: 'To do', in_progress: 'In progress', done: 'Done' })[status] || status
}

function feedDescription(item: ActivityEntry) {
  if (item.event_type === 'IssueCreated') return `Issue #${item.issue_id} was created`
  if (item.event_type === 'StatusChanged' && typeof item.payload?.status === 'string') return `Issue #${item.issue_id} moved to ${statusLabel(item.payload.status)}`
  return `Issue #${item.issue_id}: ${item.event_type.replace(/([a-z])([A-Z])/g, '$1 $2')}`
}

function ProjectCalendar({ project, issues, loading, onRangeChange, onOpenIssue }: {
  project?: Project
  issues: Issue[]
  loading: boolean
  onRangeChange: (from: string, to: string) => void
  onOpenIssue: (issue: Issue) => void
}) {
  const [month, setMonth] = useState(() => new Date(new Date().getFullYear(), new Date().getMonth(), 1))
  const notifyRef = useRef(onRangeChange)
  notifyRef.current = onRangeChange
  const year = month.getFullYear()
  const monthIndex = month.getMonth()
  const firstCell = new Date(year, monthIndex, 1 - new Date(year, monthIndex, 1).getDay())
  const days = Array.from({ length: 42 }, (_, index) => new Date(firstCell.getFullYear(), firstCell.getMonth(), firstCell.getDate() + index))

  useEffect(() => {
    notifyRef.current(formatDate(days[0]), formatDate(days[days.length - 1]))
  }, [year, monthIndex, project?.id])

  return <section className="project-calendar-page" aria-labelledby="calendar-title">
    <div className="directory-heading"><div><p className="eyebrow">Workspace / {project?.name || 'Project'}</p><h1 id="calendar-title">Schedule calendar</h1><p>Planned start and due dates, assignment dates, and actual work milestones.</p></div><div className="calendar-nav"><button type="button" className="secondary-button" aria-label="Previous month" onClick={() => setMonth(new Date(year, monthIndex - 1, 1))}>←</button><strong>{month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}</strong><button type="button" className="secondary-button" aria-label="Next month" onClick={() => setMonth(new Date(year, monthIndex + 1, 1))}>→</button><button type="button" className="secondary-button" onClick={() => setMonth(new Date(new Date().getFullYear(), new Date().getMonth(), 1))}>Today</button></div></div>
    <div className="calendar-legend"><span><i className="legend-plan"/>Planned task</span><span><i className="legend-assigned"/>Assigned</span><span><i className="legend-started"/>Started</span><span><i className="legend-completed"/>Completed</span></div>
    {loading && <p className="state-message" role="status">Loading scheduled issues…</p>}
    <div className="calendar-grid" aria-label={`${month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })} issue calendar`}>
      {['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map(day => <div className="calendar-weekday" key={day}>{day}</div>)}
      {days.map(day => {
        const dayKey = formatDate(day)
        const inMonth = day.getMonth() === monthIndex
        const scheduled = issues.filter(issue => issueOnDate(issue, dayKey))
        const milestones = issues.flatMap(issue => {
          const labels: string[] = []
          if (timestampDate(issue.assigned_at) === dayKey) labels.push('Assigned')
          if (timestampDate(issue.started_at) === dayKey) labels.push('Started')
          if (timestampDate(issue.completed_at) === dayKey) labels.push('Completed')
          return labels.length ? [{ issue, labels }] : []
        })
        return <div className={`calendar-day${inMonth ? '' : ' outside-month'}`} key={dayKey}><span className="calendar-day-number">{day.getDate()}</span>
          <div className="calendar-day-items">{scheduled.map(issue => <button type="button" className={`calendar-issue ${issue.status}`} key={issue.id} title={`${issue.title}${issue.start_date ? ` · starts ${issue.start_date}` : ''}${issue.due_date ? ` · due ${issue.due_date}` : ''}`} onClick={() => onOpenIssue(issue)}><span>#{issue.id} {issue.title}</span><small>{issue.start_date === dayKey ? 'Start' : ''}{issue.start_date === dayKey && issue.due_date === dayKey ? ' · ' : ''}{issue.due_date === dayKey ? 'Due' : ''}</small></button>)}
            {milestones.map(({ issue, labels }) => <button key={`${issue.id}-${labels.join('-')}`} type="button" className="calendar-milestone" onClick={() => onOpenIssue(issue)}><span>#{issue.id}</span>{labels.join(' · ')}</button>)}
          </div>
        </div>
      })}
    </div>
    <p className="calendar-help">Issues without a planned start or due date aren’t shown in this calendar.</p>
  </section>
}

function formatDate(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function timestampDate(value?: string | null) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value.slice(0, 10)
  return formatDate(date)
}

function issueOnDate(issue: Issue, date: string) {
  if (issue.start_date && issue.due_date) return issue.start_date <= date && issue.due_date >= date
  return issue.start_date === date || issue.due_date === date
}

function StatChip({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <span className={`stat-chip ${tone}`}><span className="stat-dot" />{label}: {value}</span>
}

function BoardToolbar({
  query,
  statusFilter,
  priorityFilter,
  view,
  sortDescending,
  onQueryChange,
  onStatusFilterChange,
  onPriorityFilterChange,
  onViewChange,
  onSortChange,
}: {
  query: string
  statusFilter: string
  priorityFilter: string
  view: 'board' | 'list'
  sortDescending: boolean
  onQueryChange: (value: string) => void
  onStatusFilterChange: (value: string) => void
  onPriorityFilterChange: (value: string) => void
  onViewChange: (value: 'board' | 'list') => void
  onSortChange: () => void
}) {
  return (
    <div className="board-toolbar">
      <div className="toolbar-filters">
        <label className="board-search">
          <span className="material-symbols-outlined" aria-hidden="true">filter_list</span>
          <input aria-label="Search issues" placeholder="Filter issues…" value={query} onChange={event => onQueryChange(event.target.value)} />
        </label>
        <label className="filter-select"><span className="visually-hidden">Filter by status</span>
          <select aria-label="Filter by status" value={statusFilter} onChange={event => onStatusFilterChange(event.target.value)}>
            <option value="">Status: All</option><option value="todo">Status: To do</option><option value="in_progress">Status: In progress</option><option value="done">Status: Done</option>
          </select>
        </label>
        <label className="filter-select"><span className="visually-hidden">Filter by priority</span>
          <select aria-label="Filter by priority" value={priorityFilter} onChange={event => onPriorityFilterChange(event.target.value)}>
            <option value="">Priority: Any</option><option value="critical">Priority: Critical</option><option value="high">Priority: High</option><option value="medium">Priority: Medium</option><option value="low">Priority: Low</option>
          </select>
        </label>
        <button className="sort-button" type="button" onClick={onSortChange} aria-label={`Sort priority ${sortDescending ? 'descending' : 'ascending'}`}>
          Sort: Priority {sortDescending ? '↓' : '↑'}<span className="material-symbols-outlined" aria-hidden="true">swap_vert</span>
        </button>
      </div>
      <div className="view-toggle" role="group" aria-label="Issue view">
        <button type="button" className={view === 'board' ? 'selected' : ''} aria-pressed={view === 'board'} onClick={() => onViewChange('board')}>
          <span className="material-symbols-outlined" aria-hidden="true">view_column</span>Board
        </button>
        <button type="button" className={view === 'list' ? 'selected' : ''} aria-pressed={view === 'list'} onClick={() => onViewChange('list')}>
          <span className="material-symbols-outlined" aria-hidden="true">format_list_bulleted</span>List
        </button>
      </div>
    </div>
  )
}

function boardIssues(issues: Issue[]) {
  return [...issues].sort((a, b) => a.position - b.position || a.id - b.id)
}

export function applyIssueMove(issues: Issue[], issueId: number, status: string, position: number): Issue[] {
  const ordered = boardIssues(issues)
  const moved = ordered.find(issue => issue.id === issueId)
  if (!moved) return issues
  const sourceStatus = moved.status
  const remaining = ordered.filter(issue => issue.id !== issueId)
  const destination = remaining.filter(issue => issue.status === status)
  const insertAt = Math.max(0, Math.min(position, destination.length))
  destination.splice(insertAt, 0, { ...moved, status })
  const updatedPositions = new Map(destination.map((issue, index) => [issue.id, index]))
  if (sourceStatus !== status) {
    remaining.filter(issue => issue.status === sourceStatus).forEach((issue, index) => updatedPositions.set(issue.id, index))
  }
  return issues.map(issue => {
    const nextPosition = updatedPositions.get(issue.id)
    return nextPosition === undefined ? issue : { ...issue, status: issue.id === issueId ? status : issue.status, position: nextPosition }
  })
}

export function KanbanBoard({ issues, onOpenIssue, onSetStatus, onDeleteIssue, currentUserID, globalRole, onAddIssue, onMoveIssue, disabled }: {
  issues: Issue[]
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  onDeleteIssue?: (issue: Issue) => void
  currentUserID?: number
  globalRole?: string
  onAddIssue: (status?: string) => void
  onMoveIssue: (issueId: number, status: string, position: number) => void
  disabled: boolean
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 150, tolerance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  const [activeIssue, setActiveIssue] = useState<Issue | null>(null)
  const [overStatus, setOverStatus] = useState<string | null>(null)
  const onDragStart = (event: DragStartEvent) => {
    setActiveIssue(issues.find(issue => issue.id === Number(event.active.id)) ?? null)
  }
  const onDragOver = (event: DragOverEvent) => {
    setOverStatus(dropStatus(event.over?.data.current, event.over?.id))
  }
  const onDragEnd = (event: DragEndEvent) => {
    const activeId = Number(event.active.id)
    const over = event.over
    setActiveIssue(null)
    setOverStatus(null)
    if (!over) return
    const move = resolveKanbanDrop(issues, activeId, Number(over.id), dropStatus(over.data.current, over.id))
    if (move) onMoveIssue(move.issueId, move.status, move.position)
  }
  const finishDrag = () => { setActiveIssue(null); setOverStatus(null) }
  return (
    <DndContext sensors={sensors} collisionDetection={closestCorners} onDragStart={onDragStart} onDragOver={onDragOver} onDragEnd={onDragEnd} onDragCancel={finishDrag}>
      <div className={`kanban-board${disabled ? ' drag-disabled' : ''}`}>
        {columns.map(column => {
          const columnIssues = issues.filter(issue => issue.status === column.id)
          return (
            <KanbanColumn key={column.id} status={column.id} label={column.label} tone={column.tone} issues={columnIssues} over={overStatus === column.id} disabled={disabled} onOpenIssue={onOpenIssue} onSetStatus={onSetStatus} canDeleteIssue={issue => globalRole === 'admin' || issue.reporter_id === currentUserID || issue.assignee_id === currentUserID} onDeleteIssue={onDeleteIssue || (() => {})} onAddIssue={onAddIssue} />
          )
        })}
      </div>
      <DragOverlay dropAnimation={null}>{activeIssue ? <IssueCard issue={activeIssue} onOpenIssue={() => {}} onSetStatus={() => {}} onDeleteIssue={() => {}} canDelete={false} overlay /> : null}</DragOverlay>
    </DndContext>
  )
}

export function resolveKanbanDrop(issues: Issue[], activeId: number, overId: number, overStatus: string | null) {
  const active = issues.find(issue => issue.id === activeId)
  if (!active) return null
  const overIssue = issues.find(issue => issue.id === overId)
  const status = overStatus || overIssue?.status
  if (!status || overIssue?.id === activeId) return null
  const targetColumn = boardIssues(issues).filter(issue => issue.status === status)
  const position = overIssue ? targetColumn.findIndex(issue => issue.id === overIssue.id) : targetColumn.length
  const fromPosition = boardIssues(issues).filter(issue => issue.status === active.status).findIndex(issue => issue.id === activeId)
  if (active.status === status && fromPosition === position) return null
  return { issueId: activeId, status, position }
}

function dropStatus(data: Record<string, unknown> | undefined, id: string | number | undefined) {
  if (typeof data?.status === 'string') return data.status
  if (typeof id === 'string' && id.startsWith('column:')) return id.slice('column:'.length)
  return null
}

function KanbanColumn({ status, label, tone, issues, over, disabled, onOpenIssue, onSetStatus, canDeleteIssue, onDeleteIssue, onAddIssue }: {
  status: Status
  label: string
  tone: string
  issues: Issue[]
  over: boolean
  disabled: boolean
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  canDeleteIssue: (issue: Issue) => boolean
  onDeleteIssue: (issue: Issue) => void
  onAddIssue: (status?: string) => void
}) {
  const { setNodeRef, isOver } = useDroppable({ id: `column:${status}`, data: { type: 'column', status }, disabled })
  return (
    <section ref={setNodeRef} className={`kanban-column ${tone}${over || isOver ? ' is-over' : ''}`} aria-label={`${label} issues`}>
      <header className="column-header">
        <div className="column-heading"><span className="column-indicator" /><h2>{label}</h2><span className="column-count">{issues.length}</span></div>
        <button className="icon-button column-menu" type="button" aria-label={`Add issue to ${label}`} title={`Add issue to ${label}`} onClick={() => onAddIssue(status)}>
          <span className="material-symbols-outlined" aria-hidden="true">add</span>
        </button>
      </header>
      <SortableContext items={issues.map(issue => issue.id)} strategy={verticalListSortingStrategy}>
      <div className="column-cards">
        {issues.map(issue => <SortableIssueCard key={issue.id} issue={issue} disabled={disabled} onOpenIssue={onOpenIssue} onSetStatus={onSetStatus} onDeleteIssue={onDeleteIssue} canDelete={canDeleteIssue(issue)} />)}
        {issues.length === 0 && <p className="empty-column">No issues</p>}
      </div>
      </SortableContext>
      <button className="add-issue-button" type="button" onClick={() => onAddIssue(status)}>
        <span className="material-symbols-outlined" aria-hidden="true">add</span>Add issue
      </button>
    </section>
  )
}

function SortableIssueCard({ issue, disabled, onOpenIssue, onSetStatus, onDeleteIssue, canDelete }: {
  issue: Issue
  disabled: boolean
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  onDeleteIssue: (issue: Issue) => void
  canDelete: boolean
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: issue.id,
    data: { type: 'issue', status: issue.status },
    disabled,
  })
  const dragHandleProps = { ...attributes, ...listeners } as React.ButtonHTMLAttributes<HTMLButtonElement> & DraggableSyntheticListeners
  return <div ref={setNodeRef} className={isDragging ? 'sortable-card is-dragging' : 'sortable-card'} style={{ transform: CSS.Transform.toString(transform), transition }}>
    <IssueCard issue={issue} onOpenIssue={onOpenIssue} onSetStatus={onSetStatus} onDeleteIssue={onDeleteIssue} canDelete={canDelete} dragHandleProps={dragHandleProps} />
  </div>
}

function IssueCard({ issue, onOpenIssue, onSetStatus, onDeleteIssue, canDelete, dragHandleProps, overlay = false }: {
  issue: Issue
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  onDeleteIssue: (issue: Issue) => void
  canDelete: boolean
  dragHandleProps?: React.ButtonHTMLAttributes<HTMLButtonElement>
  overlay?: boolean
}) {
  return (
    <article className={`issue-card ${issue.status === 'done' ? 'is-done' : ''}${overlay ? ' drag-overlay-card' : ''}`}>
      <div className="issue-card-top">
        {dragHandleProps && <button type="button" className="drag-handle" aria-label={`Drag issue ${issue.id}`} {...dragHandleProps}><span className="material-symbols-outlined" aria-hidden="true">drag_indicator</span></button>}
        <span className="issue-id">#{issue.id}</span>
        <span className={`priority-badge ${issue.priority}`}><span className="priority-icon material-symbols-outlined" aria-hidden="true">{priorityIcon(issue.priority)}</span>{capitalize(issue.priority)}</span>
        {canDelete && !overlay && <button type="button" className="issue-delete-button" aria-label={`Delete issue ${issue.id}`} title="Delete issue" onMouseDown={event => event.currentTarget.focus()} onClick={() => onDeleteIssue(issue)}><span className="material-symbols-outlined" aria-hidden="true">delete</span></button>}
      </div>
      <button className="issue-title" type="button" onClick={() => onOpenIssue(issue)}>{issue.title}</button>
      {issue.description && <p className="issue-description">{issue.description}</p>}
      <div className="issue-card-bottom">
        {issue.assignee_id ? <span className="assignee-chip">Assignee #{issue.assignee_id}</span> : <span className="assignee-chip unassigned">Unassigned</span>}
        <label className="status-control"><span className="visually-hidden">Change issue status</span>
          <select aria-label="Change issue status" value={issue.status} onChange={event => onSetStatus(issue, event.target.value)}>
            {columns.map(column => <option key={column.id} value={column.id}>{column.label}</option>)}
          </select>
        </label>
      </div>
    </article>
  )
}

function IssueList({ issues, onOpenIssue, onSetStatus, onDeleteIssue, currentUserID, globalRole }: {
  issues: Issue[]
  onOpenIssue: (issue: Issue) => void
  onSetStatus: (issue: Issue, status: string) => void
  onDeleteIssue: (issue: Issue) => void
  currentUserID: number
  globalRole: string
}) {
  return (
    <div className="issue-list" role="list" aria-label="Issues">
      {issues.map(issue => (
        <div className="issue-list-row" role="listitem" key={issue.id}>
          <span className="issue-id">#{issue.id}</span>
          <button className="issue-title" type="button" onClick={() => onOpenIssue(issue)}>{issue.title}</button>
          <span className={`priority-badge ${issue.priority}`}>{capitalize(issue.priority)}</span>
            <label className="status-control"><span className="visually-hidden">Change issue status</span>
            <select aria-label="Change issue status" value={issue.status} onChange={event => onSetStatus(issue, event.target.value)}>
              {columns.map(column => <option key={column.id} value={column.id}>{column.label}</option>)}
            </select>
            </label>
            {(globalRole === 'admin' || issue.reporter_id === currentUserID || issue.assignee_id === currentUserID) && <button type="button" className="issue-delete-button" aria-label={`Delete issue ${issue.id}`} title="Delete issue" onMouseDown={event => event.currentTarget.focus()} onClick={() => onDeleteIssue(issue)}><span className="material-symbols-outlined" aria-hidden="true">delete</span></button>}
        </div>
      ))}
    </div>
  )
}

function priorityIcon(priority: string) {
  if (priority === 'critical' || priority === 'high') return 'priority_high'
  if (priority === 'low') return 'arrow_downward'
  return 'signal_cellular_alt_2_bar'
}

function capitalize(value: string) {
  return value ? `${value[0].toUpperCase()}${value.slice(1)}` : 'Unknown'
}
