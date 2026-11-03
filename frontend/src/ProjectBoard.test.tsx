import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ActivityEntry, AnalyticsSnapshot, Issue, KanbanBoard, Project, ProjectBoard, ProjectBoardProps } from './ProjectBoard'

vi.mock('@dnd-kit/core', async () => {
  const React = await import('react')
  return {
    closestCorners: vi.fn(),
    DndContext: ({ children, onDragStart, onDragEnd }: { children: React.ReactNode; onDragStart: (event: unknown) => void; onDragEnd: (event: unknown) => void }) => React.createElement(React.Fragment, null,
      children,
      React.createElement('button', { type: 'button', onClick: () => {
        onDragStart({ active: { id: 1 } })
        onDragEnd({ active: { id: 1 }, over: { id: 2, data: { current: { type: 'issue', status: 'todo' } } } })
      } }, 'Simulate drop'),
    ),
    DragOverlay: ({ children }: { children: React.ReactNode }) => children,
    KeyboardSensor: vi.fn(),
    PointerSensor: vi.fn(),
    TouchSensor: vi.fn(),
    useDroppable: () => ({ setNodeRef: vi.fn(), isOver: false }),
    useSensor: () => ({}),
    useSensors: (...sensors: unknown[]) => sensors,
  }
})

vi.mock('@dnd-kit/sortable', () => ({
  SortableContext: ({ children }: { children: React.ReactNode }) => children,
  sortableKeyboardCoordinates: vi.fn(),
  useSortable: () => ({ attributes: {}, listeners: {}, setNodeRef: vi.fn(), transform: null, transition: undefined, isDragging: false }),
  verticalListSortingStrategy: vi.fn(),
}))

vi.mock('@dnd-kit/utilities', () => ({ CSS: { Transform: { toString: () => undefined } } }))

afterEach(cleanup)

const issues: Issue[] = [
  { id: 1, project_id: 4, title: 'First', description: '', priority: 'medium', status: 'todo', position: 0 },
  { id: 2, project_id: 4, title: 'Second', description: '', priority: 'high', status: 'todo', position: 1 },
  { id: 3, project_id: 4, title: 'Third', description: '', priority: 'low', status: 'in_progress', position: 0 },
]

describe('KanbanBoard drag/drop handler', () => {
  it('emits a final column index from the drop event handler', () => {
    const onMoveIssue = vi.fn()
    render(<KanbanBoard issues={issues} onOpenIssue={vi.fn()} onSetStatus={vi.fn()} onAddIssue={vi.fn()} onMoveIssue={onMoveIssue} disabled={false} />)

    fireEvent.click(screen.getByRole('button', { name: 'Simulate drop' }))

    expect(onMoveIssue).toHaveBeenCalledWith(1, 'todo', 1)
  })

  it('shows issue delete actions only to the creator, assignee, or platform admin', () => {
    const deletableIssues: Issue[] = [
      { ...issues[0], reporter_id: 12 },
      { ...issues[1], assignee_id: 12 },
      issues[2],
    ]
    const onDeleteIssue = vi.fn()
    render(<KanbanBoard issues={deletableIssues} onOpenIssue={vi.fn()} onSetStatus={vi.fn()} onDeleteIssue={onDeleteIssue} currentUserID={12} globalRole="member" onAddIssue={vi.fn()} onMoveIssue={vi.fn()} disabled={false} />)
    expect(screen.getByRole('button', { name: 'Delete issue 1' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Delete issue 2' })).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Delete issue 3' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Delete issue 1' }))
    expect(onDeleteIssue).toHaveBeenCalledWith(deletableIssues[0])
  })

  it('shows platform admins delete actions for every issue', () => {
    render(<KanbanBoard issues={issues} onOpenIssue={vi.fn()} onSetStatus={vi.fn()} onDeleteIssue={vi.fn()} currentUserID={99} globalRole="admin" onAddIssue={vi.fn()} onMoveIssue={vi.fn()} disabled={false} />)
    expect(screen.getAllByRole('button', { name: /Delete issue/ })).toHaveLength(issues.length)
  })
})

function workspaceProps(screenName: ProjectBoardProps['screen']): ProjectBoardProps {
  const project: Project = { id: 4, name: 'Roadmap', description: 'Release planning' }
  const activity: ActivityEntry = { id: 1, project_id: 4, issue_id: 1, actor_id: 12, event_type: 'IssueCreated', payload: {}, occurred_at: '2026-10-20T10:00:00Z' }
  const analytics: AnalyticsSnapshot = { project_id: 4, total: 1, by_status: { todo: 1, in_progress: 0, done: 0 }, by_priority: { critical: 0, high: 1, medium: 0, low: 0 }, open_high_priority: 1 }
  return {
    screen: screenName, projects: [project], adminProjects: [project], adminProjectsLoading: false, adminProjectsError: '', members: [], projectRole: 'member', globalRole: 'member', selectedProject: project, selectedProjectId: 4,
    issues, calendarIssues: [], calendarLoading: false, myIssues: [issues[0]], myIssuesLoading: false, myIssuesError: '', activityItems: [activity], activityLoading: false, activityPartialErrors: [], analytics, analyticsLoading: false, analyticsError: '', adminUsers: [], adminUsersLoading: false, adminUsersError: '', adminUsersHasMore: false, managedUserID: null, managedUserProjectIDs: [], managedUserProjectsLoading: false, managedUserProjectsError: '', currentUserID: 12,
    loading: false, error: '', query: '', statusFilter: '', priorityFilter: '', email: 'me@example.test',
    onSelectProject: vi.fn(), onShowProjects: vi.fn(), onShowCalendar: vi.fn(), onShowAdmin: vi.fn(), onShowMyIssues: vi.fn(), onShowActivity: vi.fn(), onShowAnalytics: vi.fn(), onShowChat: vi.fn(), onAskTrackerAssistant: vi.fn(async () => 'Assistant response'), onShowUsers: vi.fn(), onLoadMoreUsers: vi.fn(), onResetUserPassword: vi.fn(async () => {}), onCreateUser: vi.fn(async () => {}), onSetUserActive: vi.fn(async () => {}), onManageUserProjects: vi.fn(), onToggleUserProject: vi.fn(async () => {}), onOpenBoard: vi.fn(), onAddProject: vi.fn(), onCreateProject: vi.fn(async () => true), onDeleteProject: vi.fn(), onDeleteIssue: vi.fn(), onLoadUserDirectory: vi.fn(async () => ({ users: [{ id: 7, username: 'alex' }], next_after_id: null })), onAddIssue: vi.fn(), onLogout: vi.fn(), onDismissError: vi.fn(), onQueryChange: vi.fn(), onStatusFilterChange: vi.fn(), onPriorityFilterChange: vi.fn(), onOpenIssue: vi.fn(), onSetStatus: vi.fn(), onMoveIssue: vi.fn(), onCalendarRangeChange: vi.fn(), onSearchUsers: vi.fn(async () => []), onAddMember: vi.fn(async () => {}), onRemoveMember: vi.fn(async () => {}), onSetMemberRole: vi.fn(async () => {}), movePending: false, moveToast: '', onDismissMoveToast: vi.fn(),
  }
}

describe('workspace sidebar sections', () => {
  it('opens the read-only copilot panel without replacing the board and restores focus on Escape', async () => {
    const props = workspaceProps('board')
    render(<ProjectBoard {...props} />)
    const trigger = screen.getByRole('button', { name: 'Ask AI' })
    fireEvent.click(trigger)

    expect(await screen.findByRole('heading', { name: 'Trackline Copilot' })).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'Roadmap' })).not.toBeNull()
    expect(screen.getByText(/previous turns are not included as context/i)).not.toBeNull()
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('complementary', { name: 'AI Copilot' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('does not expose the UI components page', () => {
    const props = workspaceProps('board')
    render(<ProjectBoard {...props} />)
    expect(screen.queryByRole('button', { name: 'UI components' })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Design System' })).toBeNull()
  })

  it('shows a global admin as a synthetic member without project-role actions', () => {
    const props = workspaceProps('admin')
    props.globalRole = 'admin'
    props.members = [{ user_id: props.currentUserID, role: 'member', username: 'platform.admin', synthetic: true }]
    render(<ProjectBoard {...props} />)
    expect(screen.getByText('Platform admin')).not.toBeNull()
    expect(screen.getByText('Global admin with default project access')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull()
  })

  it('filters the admin project list and opens projects outside their explicit memberships', () => {
    const props = workspaceProps('projects')
    props.globalRole = 'admin'
    props.adminProjects = [
      { id: 4, name: 'Current project', description: 'Available to the admin', owner_id: 7, owner_username: 'creator.one' },
      { id: 9, name: 'Other project', description: 'Not currently a member', owner_id: 8, owner_username: 'creator.two' },
    ]
    render(<ProjectBoard {...props} />)
    expect(screen.getByText('2 projects')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Open current board' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Open board' })).not.toBeNull()
    expect(screen.getAllByRole('button', { name: /Delete project/ })).toHaveLength(2)
    fireEvent.change(screen.getByRole('textbox', { name: 'Search projects' }), { target: { value: 'creator.two' } })
    expect(screen.getByText('1 of 2 projects')).not.toBeNull()
    expect(screen.queryByText('creator.one')).toBeNull()
    expect(screen.getByText('creator.two')).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Clear project search' }))
    expect(screen.getByText('2 projects')).not.toBeNull()
  })

  it('offers project deletion only to the project creator or a platform admin', () => {
    const props = workspaceProps('projects')
    props.projects = [{ ...props.projects[0], owner_id: props.currentUserID }]
    render(<ProjectBoard {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete project Roadmap' }))
    expect(screen.getByRole('alertdialog')).not.toBeNull()
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Delete project' }))
    expect(props.onDeleteProject).toHaveBeenCalledWith(props.projects[0])
    cleanup()
    props.projects = [{ ...props.projects[0], owner_id: 7 }]
    render(<ProjectBoard {...props} />)
    expect(screen.queryByRole('button', { name: 'Delete project Roadmap' })).toBeNull()
  })

  it('keeps focus within the delete confirmation and returns focus on cancel', async () => {
    const props = workspaceProps('projects')
    props.projects = [{ ...props.projects[0], owner_id: props.currentUserID }]
    render(<ProjectBoard {...props} />)
    const trigger = screen.getByRole('button', { name: 'Delete project Roadmap' })
    trigger.focus()
    fireEvent.click(trigger)
    const dialog = screen.getByRole('alertdialog')
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
    const confirm = within(dialog).getByRole('button', { name: 'Delete project' })
    expect(document.activeElement).toBe(cancel)
    fireEvent.keyDown(window, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(confirm)
    fireEvent.keyDown(window, { key: 'Tab' })
    expect(document.activeElement).toBe(cancel)
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
    expect(props.onDeleteProject).not.toHaveBeenCalled()
  })

  it('keeps focus in the confirmation while deletion is pending', async () => {
    const props = workspaceProps('projects')
    props.projects = [{ ...props.projects[0], owner_id: props.currentUserID }]
    let finishDelete!: () => void
    props.onDeleteProject = vi.fn(() => new Promise<void>(resolve => { finishDelete = resolve }))
    render(<ProjectBoard {...props} />)
    const trigger = screen.getByRole('button', { name: 'Delete project Roadmap' })
    trigger.focus()
    fireEvent.click(trigger)
    const dialog = screen.getByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete project' }))
    await waitFor(() => expect((within(dialog).getByRole('button', { name: 'Deleting…' }) as HTMLButtonElement).disabled).toBe(true))
    fireEvent.keyDown(window, { key: 'Tab' })
    expect(document.activeElement).toBe(dialog)
    finishDelete()
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('shows an Add members shortcut to project managers and global admins only', async () => {
    const memberProps = workspaceProps('board')
    render(<ProjectBoard {...memberProps} />)
    expect(screen.queryByRole('button', { name: 'Add members' })).toBeNull()
    cleanup()

    const managerProps = workspaceProps('board')
    managerProps.projectRole = 'admin'
    const addMember = vi.fn(async () => {})
    managerProps.members = [{ user_id: 8, role: 'member', username: 'existing' }, { user_id: 99, role: 'member', username: 'inactive', active: false }]
    managerProps.onLoadUserDirectory = vi.fn(async () => ({ users: [{ id: 7, username: 'alex' }, { id: 8, username: 'existing' }], next_after_id: null }))
    managerProps.onAddMember = addMember
    render(<ProjectBoard {...managerProps} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add members' }))
    expect(managerProps.onShowAdmin).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog', { name: 'Add members' })).not.toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close add members' }))
    fireEvent.keyDown(window, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }))
    const alexCard = await screen.findByRole('button', { name: /alex, not selected/ })
    const existingCard = screen.getByRole('button', { name: /existing, already a member/ })
    const inactiveCard = screen.getByRole('button', { name: /inactive, already a member/ })
    expect((existingCard as HTMLButtonElement).disabled).toBe(true)
    expect(existingCard.getAttribute('aria-pressed')).toBe('true')
    expect((inactiveCard as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(alexCard)
    expect(screen.getByRole('button', { name: /alex, selected/ }).getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: 'Add 1 member' }))
    await waitFor(() => expect(addMember).toHaveBeenCalledWith(7))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add members' })))
    cleanup()

    const globalAdminProps = workspaceProps('board')
    globalAdminProps.globalRole = 'admin'
    render(<ProjectBoard {...globalAdminProps} />)
    expect(screen.getByRole('button', { name: 'Add members' })).not.toBeNull()
  })

  it('makes each breadcrumb navigate to its corresponding view', () => {
    const props = workspaceProps('admin')
    render(<ProjectBoard {...props} />)
    const breadcrumb = within(screen.getByRole('navigation', { name: 'Breadcrumb' }))
    fireEvent.click(breadcrumb.getByRole('button', { name: 'Projects' }))
    fireEvent.click(breadcrumb.getByRole('button', { name: 'Roadmap' }))
    fireEvent.click(breadcrumb.getByRole('button', { name: 'Project team' }))
    expect(props.onShowProjects).toHaveBeenCalled()
    expect(props.onOpenBoard).toHaveBeenCalled()
    expect(props.onShowAdmin).toHaveBeenCalled()
  })

  it('lets project creators select active users to include', async () => {
    const props = workspaceProps('projects')
    const createProject = vi.fn<(event: React.FormEvent<HTMLFormElement>, memberIDs: number[]) => Promise<boolean>>(async () => true)
    props.onCreateProject = createProject
    render(<ProjectBoard {...props} />)
    const memberCheckbox = await screen.findByRole('checkbox', { name: /alex/ })
    fireEvent.click(memberCheckbox)
    expect((memberCheckbox as HTMLInputElement).checked).toBe(true)
    fireEvent.change(screen.getByPlaceholderText('Project name'), { target: { value: 'Launch' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))
    await waitFor(() => expect(createProject).toHaveBeenCalled())
    expect(createProject.mock.calls[0][1]).toEqual([7])
  })

  it('prevents selecting more than 50 project members', async () => {
    const props = workspaceProps('projects')
    props.onLoadUserDirectory = vi.fn(async () => ({ users: Array.from({ length: 51 }, (_, index) => ({ id: index + 100, username: `user${index + 1}` })), next_after_id: null }))
    render(<ProjectBoard {...props} />)
    const checkboxes = await screen.findAllByRole('checkbox')
    for (const checkbox of checkboxes.slice(0, 50)) fireEvent.click(checkbox)
    expect(screen.getByText('50 selected')).not.toBeNull()
    expect((checkboxes[50] as HTMLInputElement).disabled).toBe(true)
  })

  it('keeps project creation in the projects directory, not on the project board', () => {
    render(<ProjectBoard {...workspaceProps('board')} />)
    expect(screen.queryByRole('button', { name: 'Create project' })).toBeNull()
    cleanup()

    const projectProps = workspaceProps('projects')
    projectProps.error = 'Could not create project'
    render(<ProjectBoard {...projectProps} />)
    expect(screen.getByRole('heading', { name: 'Projects Directory' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Create project' })).not.toBeNull()
    expect(screen.getByRole('alert').textContent).toContain('Could not create project')
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(projectProps.onDismissError).toHaveBeenCalled()
  })

  it('enables navigation to My issues, Activity, and Analytics', () => {
    const props = workspaceProps('my-issues')
    render(<ProjectBoard {...props} />)
    const workspaceNav = within(screen.getByRole('navigation', { name: 'Workspace' }))
    expect((workspaceNav.getByRole('button', { name: 'My issues' }) as HTMLButtonElement).disabled).toBe(false)
    expect((workspaceNav.getByRole('button', { name: 'Activity' }) as HTMLButtonElement).disabled).toBe(false)
    expect((workspaceNav.getByRole('button', { name: 'Analytics' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.getByRole('heading', { name: 'My issues' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'First' })).not.toBeNull()
  })

  it('renders recent project activity and project analytics snapshots', () => {
    const activityProps = workspaceProps('activity')
    const { unmount } = render(<ProjectBoard {...activityProps} />)
    expect(screen.getByRole('heading', { name: 'Activity' })).not.toBeNull()
    expect(screen.getByText('Issue #1 was created')).not.toBeNull()
    unmount()

    render(<ProjectBoard {...workspaceProps('analytics')} />)
    expect(screen.getByRole('heading', { name: 'Analytics' })).not.toBeNull()
    expect(screen.getByText('Open high priority')).not.toBeNull()
    expect(screen.getByText('Counts reflect current issue state, not a historical trend or velocity report.')).not.toBeNull()
  })

  it('shows user management only to platform admins', () => {
    const memberProps = workspaceProps('board')
    render(<ProjectBoard {...memberProps} />)
    expect(screen.queryByRole('button', { name: 'Users' })).toBeNull()
    cleanup()

    const adminProps = workspaceProps('users')
    adminProps.globalRole = 'admin'
    adminProps.adminUsers = [{ id: 30, email: 'member@example.test', role: 'member', created_at: '2026-10-01T12:00:00Z', password_reset_required: false, active: true }]
    adminProps.managedUserID = 30
    adminProps.managedUserProjectIDs = [4]
    render(<ProjectBoard {...adminProps} />)
    expect(within(screen.getByRole('navigation', { name: 'Workspace' })).getByRole('button', { name: 'Users' })).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'Users & projects' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Reset password' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Create user' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Refresh projects' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Deactivate' })).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'Project access' })).not.toBeNull()
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Refresh projects' }))
    expect(adminProps.onManageUserProjects).toHaveBeenCalledWith(30)
    fireEvent.click(screen.getByRole('checkbox'))
    expect(adminProps.onToggleUserProject).toHaveBeenCalledWith(30, 4, false)
  })

  it('shows member usernames when available and keeps an ID fallback', () => {
    const props = workspaceProps('admin')
    props.projectRole = 'admin'
    props.members = [{ user_id: 42, role: 'admin', username: 'alex' }, { user_id: 43, role: 'member' }]
    render(<ProjectBoard {...props} />)
    expect(screen.getByText('alex')).not.toBeNull()
    expect(screen.getByText('User #43')).not.toBeNull()
    expect(screen.getByText('Can manage the project')).not.toBeNull()
  })
})
