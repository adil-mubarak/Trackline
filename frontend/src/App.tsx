import { FormEvent, useEffect, useRef, useState } from 'react'
import { ActivityEntry, AdminUser, AnalyticsSnapshot, applyIssueMove, Issue, Project, ProjectBoard, ProjectMember, UserDirectoryPage, UserSuggestion } from './ProjectBoard'
import ChatPage from './ChatPage'

const apiBaseURL = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')

const api = async (path: string, options: RequestInit = {}) => {
  const token = localStorage.getItem('token')
  const response = await fetch(`${apiBaseURL}/api${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options.headers,
    },
  })
  if (!response.ok) throw new Error((await response.text()) || 'Request failed')
  if (response.status === 204) return null
  return response.json()
}

async function mapConcurrent<T, R>(items: T[], concurrency: number, mapper: (item: T) => Promise<R>): Promise<R[]> {
  const results = new Array<R>(items.length)
  let next = 0
  await Promise.all(Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (next < items.length) {
      const index = next++
      results[index] = await mapper(items[index])
    }
  }))
  return results
}

type AuthMode = 'login' | 'register' | 'forgot'

function userContextFromToken(token: string): { userID: number; role: string; mustChangePassword: boolean } {
  try {
    const payload = token.split('.')[1]
    if (!payload) return { userID: 0, role: 'member', mustChangePassword: false }
    const decoded = atob(payload.replace(/-/g, '+').replace(/_/g, '/'))
    const claims = JSON.parse(decoded) as { sub?: unknown; role?: unknown; must_change_password?: unknown }
    const userID = Number(claims.sub)
    const role = claims.role === 'admin' || claims.role === 'member' ? claims.role : 'member'
    return { userID: Number.isSafeInteger(userID) && userID > 0 ? userID : 0, role, mustChangePassword: claims.must_change_password === true }
  } catch {
    return { userID: 0, role: 'member', mustChangePassword: false }
  }
}

function initialUserContext() {
  const storedUserID = Number(localStorage.getItem('user_id'))
  const fromToken = userContextFromToken(localStorage.getItem('token') || '')
  const hasStoredUserID = Number.isSafeInteger(storedUserID) && storedUserID > 0
  return {
    userID: fromToken.userID || (hasStoredUserID ? storedUserID : 0),
    role: fromToken.userID ? fromToken.role : localStorage.getItem('role') || fromToken.role,
    mustChangePassword: fromToken.mustChangePassword,
  }
}

export default function App() {
  const [token, setToken] = useState(localStorage.getItem('token') || '')
  const [mode, setMode] = useState<AuthMode>('login')
  const [showPassword, setShowPassword] = useState(false)
  const [projects, setProjects] = useState<Project[]>([])
  const [members, setMembers] = useState<ProjectMember[]>([])
  const [calendarIssues, setCalendarIssues] = useState<Issue[]>([])
  const [calendarLoading, setCalendarLoading] = useState(false)
  const [myIssues, setMyIssues] = useState<Issue[]>([])
  const [myIssuesLoading, setMyIssuesLoading] = useState(false)
  const [myIssuesErrors, setMyIssuesErrors] = useState<string[]>([])
  const [activityItems, setActivityItems] = useState<ActivityEntry[]>([])
  const [activityLoading, setActivityLoading] = useState(false)
  const [activityPartialErrors, setActivityPartialErrors] = useState<string[]>([])
  const [analytics, setAnalytics] = useState<AnalyticsSnapshot | null>(null)
  const [analyticsLoading, setAnalyticsLoading] = useState(false)
  const [analyticsError, setAnalyticsError] = useState('')
  const [adminUsers, setAdminUsers] = useState<AdminUser[]>([])
  const [adminProjects, setAdminProjects] = useState<Project[]>([])
  const [adminProjectsLoading, setAdminProjectsLoading] = useState(false)
  const [adminProjectsError, setAdminProjectsError] = useState('')
  const [adminUsersLoading, setAdminUsersLoading] = useState(false)
  const [adminUsersError, setAdminUsersError] = useState('')
  const [adminUsersAfterID, setAdminUsersAfterID] = useState<number | null>(null)
  const [adminUsersHasMore, setAdminUsersHasMore] = useState(false)
  const [managedUserID, setManagedUserID] = useState<number | null>(null)
  const [managedUserProjectIDs, setManagedUserProjectIDs] = useState<number[]>([])
  const [managedUserProjectsLoading, setManagedUserProjectsLoading] = useState(false)
  const [managedUserProjectsError, setManagedUserProjectsError] = useState('')
  const [userContext] = useState(initialUserContext)
  const [currentUserID, setCurrentUserID] = useState(userContext.userID)
  const [globalRole, setGlobalRole] = useState(userContext.role)
  const [mustChangePassword, setMustChangePassword] = useState(userContext.mustChangePassword)
  const [selected, setSelected] = useState<number | null>(null)
  const [issues, setIssues] = useState<Issue[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [query, setQuery] = useState(() => new URLSearchParams(location.search).get('q') || '')
  const [statusFilter, setStatusFilter] = useState(() => new URLSearchParams(location.search).get('status') || '')
  const [priorityFilter, setPriorityFilter] = useState(() => new URLSearchParams(location.search).get('priority') || '')
  const [selectedIssue, setSelectedIssue] = useState<Issue | null>(null)
  const [loadingIssues, setLoadingIssues] = useState(false)
  const [newIssueStatus, setNewIssueStatus] = useState('todo')
  const [createIssueOpen, setCreateIssueOpen] = useState(false)
  const [movePending, setMovePending] = useState(false)
  const [moveToast, setMoveToast] = useState('')
  const [screen, setScreen] = useState<'board' | 'projects' | 'calendar' | 'admin' | 'my-issues' | 'activity' | 'analytics' | 'users' | 'chat'>('board')
  const projectRequestId = useRef(0)
  const issueRequestId = useRef(0)
  const myIssuesRequestId = useRef(0)
  const activityRequestId = useRef(0)
  const analyticsRequestId = useRef(0)
  const managedUserProjectsRequestId = useRef(0)
  const adminProjectsRequestID = useRef(0)
  const movePendingRef = useRef(false)
  const selectedProjectRef = useRef<number | null>(selected)
  selectedProjectRef.current = selected

  const selectedProject = projects.find(project => project.id === selected)
    || (globalRole === 'admin' ? adminProjects.find(project => project.id === selected) : undefined)

  async function loadProjects() {
    const requestId = ++projectRequestId.current
    let data: Project[]
    try {
      data = await api('/projects') as Project[]
    } catch (cause) {
      if (requestId !== projectRequestId.current) return
      throw cause
    }
    if (requestId !== projectRequestId.current) return
    setProjects(data)
    if (!selected && data.length) {
      selectedProjectRef.current = data[0].id
      setSelected(data[0].id)
    }
  }

  async function loadAdminProjects() {
    const requestID = ++adminProjectsRequestID.current
    setAdminProjectsLoading(true)
    setAdminProjectsError('')
    try {
      const result = await api('/projects?scope=all') as Project[]
      const ownerIDs = [...new Set(result.map(project => project.owner_id).filter((id): id is number => typeof id === 'number' && Number.isSafeInteger(id) && id > 0))]
      const ownerNames = new Map<number, string>()
      for (let start = 0; start < ownerIDs.length; start += 100) {
        try {
          const names = await api('/users/names', { method: 'POST', body: JSON.stringify({ user_ids: ownerIDs.slice(start, start + 100) }) }) as Array<{ id: number; username: string }>
          for (const owner of names) ownerNames.set(owner.id, owner.username)
        } catch {
          // Project visibility should not depend on optional owner-name enrichment.
        }
      }
      const projectsWithOwners = result.map(project => ({ ...project, owner_username: project.owner_id ? ownerNames.get(project.owner_id) : undefined }))
      if (requestID === adminProjectsRequestID.current) setAdminProjects(projectsWithOwners)
    } catch (cause) {
      if (requestID === adminProjectsRequestID.current) setAdminProjectsError(cause instanceof Error ? cause.message : 'Could not load all projects.')
    } finally {
      if (requestID === adminProjectsRequestID.current) setAdminProjectsLoading(false)
    }
  }

  async function loadIssues(projectId: number) {
    const requestId = ++issueRequestId.current
    setLoadingIssues(true)
    try {
      const params = new URLSearchParams({ project_id: String(projectId) })
      if (query) params.set('q', query)
      if (statusFilter) params.set('status', statusFilter)
      if (priorityFilter) params.set('priority', priorityFilter)
      const result = await api(`/issues?${params}`) as Issue[]
      if (requestId === issueRequestId.current) {
        setIssues(result)
        setError('')
      }
    } catch (cause) {
      if (requestId === issueRequestId.current) {
        setError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      if (requestId === issueRequestId.current) setLoadingIssues(false)
    }
  }

  async function fetchProjectMembers(projectId: number) {
	const members = await api(`/projects/${projectId}/members`) as ProjectMember[]
	const ids = members.map(member => member.user_id).filter(id => id > 0)
	if (ids.length === 0) return members
	const byID = new Map<number, { username: string; active: boolean }>()
	for (let start = 0; start < ids.length; start += 100) {
		try {
			const names = await api('/users/names', { method: 'POST', body: JSON.stringify({ user_ids: ids.slice(start, start + 100) }) }) as Array<{ id: number; username: string; active: boolean }>
			for (const user of names) byID.set(user.id, { username: user.username, active: user.active })
		} catch {
			// Keep project membership usable if an account-name batch is unavailable.
		}
	}
	return members.map(member => {
		const user = byID.get(member.user_id)
		return { ...member, username: user?.username, active: user?.active }
	})
  }

  async function loadProjectMembers(projectId: number) {
    const result = await fetchProjectMembers(projectId)
    if (selectedProjectRef.current === projectId) setMembers(result)
    return result
  }

  useEffect(() => {
    if (token && !mustChangePassword) loadProjects().catch(cause => setError(cause.message))
  }, [token, mustChangePassword])

  useEffect(() => {
    const params = new URLSearchParams(location.search)
    query ? params.set('q', query) : params.delete('q')
    statusFilter ? params.set('status', statusFilter) : params.delete('status')
    priorityFilter ? params.set('priority', priorityFilter) : params.delete('priority')
    const search = params.toString()
    history.replaceState(null, '', `${location.pathname}${search ? `?${search}` : ''}`)
  }, [query, statusFilter, priorityFilter])

  useEffect(() => {
    if (token && !mustChangePassword && selected) void loadIssues(selected)
  }, [token, mustChangePassword, selected, query, statusFilter, priorityFilter])

  useEffect(() => {
    if (token && !mustChangePassword && selected) {
      loadProjectMembers(selected).catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
    } else {
      setMembers([])
    }
  }, [token, mustChangePassword, selected])

  async function authenticate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError('')
    const form = new FormData(event.currentTarget)
    try {
      const data = await api(`/auth/${mode}`, {
        method: 'POST',
        body: JSON.stringify({ email: form.get('email'), password: form.get('password') }),
      })
      if (mode === 'register') {
        setMode('login')
        return
      }
      localStorage.setItem('email', String(form.get('email') || ''))
      localStorage.setItem('token', data.access_token)
      localStorage.setItem('user_id', String(data.user_id))
      localStorage.setItem('role', String(data.role || 'member'))
      setCurrentUserID(Number(data.user_id) || 0)
      setGlobalRole(String(data.role || 'member'))
      setMustChangePassword(Boolean(data.must_change_password))
      setToken(data.access_token)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not sign in')
    } finally {
      setBusy(false)
    }
  }

  async function createProject(event: FormEvent<HTMLFormElement>, memberIDs: number[]) {
    event.preventDefault()
    const target = event.currentTarget
    const form = new FormData(target)
    setError('')
    try {
      await api('/projects', {
        method: 'POST',
        body: JSON.stringify({ name: form.get('name'), description: form.get('description') || '', member_ids: memberIDs }),
      })
      target.reset()
      await loadProjects()
      if (globalRole === 'admin') await loadAdminProjects()
      return true
    } catch (cause) {
      setError(String(cause))
      return false
    }
  }

  async function deleteProject(project: Project) {
    setError('')
    try {
      await api(`/projects/${project.id}`, { method: 'DELETE' })
      setProjects(current => current.filter(item => item.id !== project.id))
      setAdminProjects(current => current.filter(item => item.id !== project.id))
      setManagedUserProjectIDs(current => current.filter(id => id !== project.id))
      if (selectedProjectRef.current === project.id) {
        selectedProjectRef.current = null
        setSelected(null)
        setIssues([])
        setMembers([])
      }
    } catch (cause) {
      throw cause instanceof Error ? cause : new Error('Could not delete project.')
    }
  }

  async function createIssue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const target = event.currentTarget
    const form = new FormData(target)
    const projectId = Number(form.get('project_id')) || selected
    if (!projectId) return
    const labelNames = String(form.get('labels') || '').split(',').map(name => name.trim()).filter(Boolean)
    try {
      const created = await api('/issues', {
        method: 'POST',
        body: JSON.stringify({
          project_id: projectId,
          title: form.get('title'),
          description: form.get('description') || '',
          priority: form.get('priority'),
          status: form.get('status'),
          assignee_id: String(form.get('assignee_id') || '').trim() ? Number(form.get('assignee_id')) : null,
          start_date: form.get('start_date') || null,
          due_date: form.get('due_date') || null,
        }),
      }) as Issue
      const labelResults = await Promise.allSettled(labelNames.map(name => api(`/issues/${created.id}/labels`, {
        method: 'POST',
        body: JSON.stringify({ name }),
      })))
      target.reset()
      setNewIssueStatus('todo')
      setCreateIssueOpen(false)
      if (projectId === selectedProjectRef.current) await loadIssues(projectId)
      else selectProject(projectId)
      if (labelResults.some(result => result.status === 'rejected')) setError('Issue created, but one or more labels could not be added.')
    } catch (cause) {
      setError(String(cause))
    }
  }

  async function setStatus(issue: Issue, status: string) {
    try {
      await api(`/issues/${issue.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          title: issue.title,
          description: issue.description,
          priority: issue.priority,
          status,
          assignee_id: issue.assignee_id,
          start_date: issue.start_date || null,
          due_date: issue.due_date || null,
        }),
      })
      const currentProjectId = selectedProjectRef.current
      if (currentProjectId) await loadIssues(currentProjectId)
    } catch (cause) {
      setError(String(cause))
    }
  }

  async function deleteIssue(issue: Issue) {
    setError('')
    try {
      await api(`/issues/${issue.id}`, { method: 'DELETE' })
      setIssues(current => current.filter(item => item.id !== issue.id))
      setCalendarIssues(current => current.filter(item => item.id !== issue.id))
      setMyIssues(current => current.filter(item => item.id !== issue.id))
      setActivityItems(current => current.filter(item => item.issue_id !== issue.id))
      setSelectedIssue(current => current?.id === issue.id ? null : current)
    } catch (cause) {
      throw cause instanceof Error ? cause : new Error('Could not delete issue.')
    }
  }

  async function moveIssue(issueId: number, status: string, position: number) {
    const projectId = selectedProjectRef.current
    if (!projectId || movePendingRef.current) return
    const previousIssues = issues
    setMoveToast('')
    setIssues(current => applyIssueMove(current, issueId, status, position))
    movePendingRef.current = true
    setMovePending(true)
    try {
      const updated = await api(`/projects/${projectId}/issues/${issueId}/move`, {
        method: 'PATCH',
        body: JSON.stringify({ status, position }),
      }) as Issue
      if (selectedProjectRef.current === projectId) {
        setIssues(current => current.map(issue => issue.id === issueId ? { ...issue, ...updated } : issue))
      }
    } catch (cause) {
      if (selectedProjectRef.current === projectId) {
        setIssues(previousIssues)
        setMoveToast(cause instanceof Error ? `Couldn’t move issue: ${cause.message}` : 'Couldn’t move issue. Please try again.')
      }
    } finally {
      movePendingRef.current = false
      setMovePending(false)
    }
  }

  async function loadCalendarRange(from: string, to: string) {
    const projectId = selectedProjectRef.current
    if (!projectId) {
      setCalendarIssues([])
      return
    }
    setCalendarLoading(true)
    try {
      const params = new URLSearchParams({ project_id: String(projectId), from, to })
      setCalendarIssues(await api(`/issues/calendar?${params}`) as Issue[])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      setCalendarIssues([])
    } finally {
      setCalendarLoading(false)
    }
  }

  async function loadMyIssues() {
    const requestID = ++myIssuesRequestId.current
    setMyIssuesLoading(true)
    setMyIssues([])
    setMyIssuesErrors([])
    try {
      const results = await mapConcurrent(projects, 3, async project => {
        const projectIssues: Issue[] = []
        let cursor: number | undefined
        for (let pageNumber = 0; pageNumber < 100; pageNumber++) {
          const params = new URLSearchParams({ project_id: String(project.id), assignee_id: String(currentUserID), limit: '50', sort: 'id', direction: 'desc' })
          if (cursor !== undefined) params.set('after_id', String(cursor))
          const page = await api(`/issues?${params}`) as Issue[]
          projectIssues.push(...page)
          if (page.length < 50) break
          const nextCursor = Math.min(...page.map(issue => issue.id))
          if (cursor === nextCursor) break
          cursor = nextCursor
        }
        return projectIssues
      })
      if (requestID === myIssuesRequestId.current) setMyIssues(results.flat().sort((a, b) => b.id - a.id))
    } catch (cause) {
      if (requestID === myIssuesRequestId.current) setMyIssuesErrors([cause instanceof Error ? cause.message : String(cause)])
    } finally {
      if (requestID === myIssuesRequestId.current) setMyIssuesLoading(false)
    }
  }

  async function loadActivity() {
    const requestID = ++activityRequestId.current
    setActivityLoading(true)
    setActivityItems([])
    setActivityPartialErrors([])
    const results = await mapConcurrent(projects, 3, async project => {
      try {
        const events = await api(`/activity?project_id=${project.id}&limit=100`) as ActivityEntry[]
        return { projectName: project.name, events, failed: false }
      } catch {
        return { projectName: project.name, events: [] as ActivityEntry[], failed: true }
      }
    })
    if (requestID === activityRequestId.current) {
      setActivityItems(results.flatMap(result => result.events).sort((a, b) => Date.parse(b.occurred_at) - Date.parse(a.occurred_at)))
      setActivityPartialErrors(results.filter(result => result.failed).map(result => result.projectName))
      setActivityLoading(false)
    }
  }

  async function loadAnalytics() {
    const requestID = ++analyticsRequestId.current
    const projectID = selectedProjectRef.current
    setAnalytics(null)
    setAnalyticsError('')
    if (!projectID) {
      setAnalyticsLoading(false)
      return
    }
    setAnalyticsLoading(true)
    try {
      const result = await api(`/issues/analytics?project_id=${projectID}`) as AnalyticsSnapshot
      if (requestID === analyticsRequestId.current) setAnalytics(result)
    } catch (cause) {
      if (requestID === analyticsRequestId.current) setAnalyticsError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (requestID === analyticsRequestId.current) setAnalyticsLoading(false)
    }
  }

  async function loadAdminUsers(afterID: number | null = null) {
    setAdminUsersLoading(true)
    setAdminUsersError('')
    try {
      const params = new URLSearchParams({ limit: '50' })
      if (afterID !== null) params.set('after_id', String(afterID))
      const page = await api(`/users?${params}`) as AdminUser[]
      setAdminUsers(current => {
        const combined = afterID === null ? page : [...current, ...page]
        return Array.from(new Map(combined.map(user => [user.id, user])).values()).sort((a, b) => a.id - b.id)
      })
      setAdminUsersAfterID(page.length ? page[page.length - 1].id : afterID)
      setAdminUsersHasMore(page.length === 50)
    } catch (cause) {
      setAdminUsersError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setAdminUsersLoading(false)
    }
  }

  async function resetUserPassword(userID: number, temporaryPassword: string) {
    await api(`/users/${userID}/password-reset`, { method: 'POST', body: JSON.stringify({ temporary_password: temporaryPassword }) })
    setAdminUsers(current => current.map(user => user.id === userID ? { ...user, password_reset_required: true } : user))
  }

  async function createAdminUser(email: string, temporaryPassword: string) {
    const user = await api('/users', { method: 'POST', body: JSON.stringify({ email, temporary_password: temporaryPassword }) }) as { id: number; email: string; role: 'member'; active: boolean; must_change_password: boolean }
    const newUser: AdminUser = { id: user.id, email: user.email, role: user.role, active: user.active, password_reset_required: user.must_change_password, created_at: new Date().toISOString() }
    setAdminUsers(current => [newUser, ...current.filter(existing => existing.id !== newUser.id)])
  }

  async function setAdminUserActive(userID: number, active: boolean) {
    await api(`/users/${userID}/status`, { method: 'PATCH', body: JSON.stringify({ active }) })
    setAdminUsers(current => current.map(user => user.id === userID ? { ...user, active } : user))
  }

  async function manageUserProjects(userID: number) {
	const requestID = ++managedUserProjectsRequestId.current
    setManagedUserID(userID)
    setManagedUserProjectIDs([])
    setManagedUserProjectsLoading(true)
    setManagedUserProjectsError('')
    try {
      const availableProjects = adminProjects.length ? adminProjects : await api('/projects?scope=all') as Project[]
      if (adminProjects.length === 0) setAdminProjects(availableProjects)
      const membershipRows = await mapConcurrent(availableProjects, 4, async project => {
        const members = await api(`/projects/${project.id}/members`) as ProjectMember[]
        return [project.id, members.some(member => member.user_id === userID)] as const
      })
	  if (managedUserProjectsRequestId.current === requestID) {
		setManagedUserProjectIDs(membershipRows.filter(([, isMember]) => isMember).map(([projectID]) => projectID))
	  }
    } catch (cause) {
	  if (managedUserProjectsRequestId.current === requestID) setManagedUserProjectsError(cause instanceof Error ? cause.message : 'Could not load project memberships.')
    } finally {
	  if (managedUserProjectsRequestId.current === requestID) setManagedUserProjectsLoading(false)
    }
  }

  async function toggleAdminUserProject(userID: number, projectID: number, add: boolean) {
	const requestID = managedUserProjectsRequestId.current
    if (add) {
      await api(`/projects/${projectID}/members`, { method: 'POST', body: JSON.stringify({ user_id: userID }) })
	  if (managedUserProjectsRequestId.current === requestID) setManagedUserProjectIDs(current => current.includes(projectID) ? current : [...current, projectID])
    } else {
      await api(`/projects/${projectID}/members?user_id=${userID}`, { method: 'DELETE' })
	  if (managedUserProjectsRequestId.current === requestID) setManagedUserProjectIDs(current => current.filter(id => id !== projectID))
    }
  }

  async function changeRequiredPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError('')
    const form = new FormData(event.currentTarget)
    if (form.get('new_password') !== form.get('confirm_password')) {
      setError('The passwords do not match.')
      setBusy(false)
      return
    }
    try {
      const data = await api('/auth/password', { method: 'POST', body: JSON.stringify({ new_password: form.get('new_password') }) })
      localStorage.setItem('token', data.access_token)
      localStorage.setItem('user_id', String(data.user_id))
      localStorage.setItem('role', String(data.role || 'member'))
      setCurrentUserID(Number(data.user_id) || 0)
      setGlobalRole(String(data.role || 'member'))
      setToken(data.access_token)
      setMustChangePassword(false)
      setError('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update password')
    } finally {
      setBusy(false)
    }
  }

  function showMyIssues() {
    setScreen('my-issues')
    void loadMyIssues()
  }

  function showActivity() {
    setScreen('activity')
    void loadActivity()
  }

  function showAnalytics() {
    setScreen('analytics')
    void loadAnalytics()
  }

  function showUsers() {
    setScreen('users')
    void loadAdminUsers()
    void loadAdminProjects()
  }

  async function searchUsers(prefix: string) {
    const result = await api(`/users/autocomplete?q=${encodeURIComponent(prefix)}`) as UserSuggestion[]
    return result
  }

  async function askTrackerAssistant(message: string) {
    const result = await api('/ai/chat', { method: 'POST', body: JSON.stringify({ message }) }) as { reply: string; model: string }
    return result.reply
  }

  async function loadUserDirectory(afterID: number | null): Promise<UserDirectoryPage> {
    const params = new URLSearchParams({ limit: '25' })
    if (afterID !== null) params.set('after_id', String(afterID))
    return await api(`/users/directory?${params}`) as UserDirectoryPage
  }

  async function addProjectMember(userID: number) {
    if (!selected) return
    await api(`/projects/${selected}/members`, { method: 'POST', body: JSON.stringify({ user_id: userID }) })
    await loadProjectMembers(selected)
  }

  async function removeProjectMember(userID: number) {
    if (!selected) return
    await api(`/projects/${selected}/members?user_id=${userID}`, { method: 'DELETE' })
    await loadProjectMembers(selected)
  }

  async function setProjectMemberRole(userID: number, role: 'admin' | 'member') {
    if (!selected) return
    await api(`/projects/${selected}/members`, { method: 'PATCH', body: JSON.stringify({ user_id: userID, role }) })
    await loadProjectMembers(selected)
  }

  function addProjectFocus() {
    showProjects()
    window.setTimeout(() => document.getElementById('project-name')?.focus(), 0)
  }

  function showProjects() {
    setScreen('projects')
    if (globalRole === 'admin') void loadAdminProjects()
  }

  function selectProject(projectId: number) {
    selectedProjectRef.current = projectId
    setMembers([])
    setSelected(projectId)
  }

  function openIssue(issue: Issue) {
    if (issue.project_id && issue.project_id !== selectedProjectRef.current) selectProject(issue.project_id)
    setSelectedIssue(issue)
  }

  function addIssueFocus(status = 'todo') {
    setError('')
    setNewIssueStatus(status)
    setCreateIssueOpen(true)
  }

  function logout() {
    projectRequestId.current++
    issueRequestId.current++
    myIssuesRequestId.current++
    activityRequestId.current++
    analyticsRequestId.current++
    managedUserProjectsRequestId.current++
    localStorage.removeItem('token')
    localStorage.removeItem('email')
    localStorage.removeItem('user_id')
    localStorage.removeItem('role')
    setCurrentUserID(0)
    setGlobalRole('member')
    setMustChangePassword(false)
    selectedProjectRef.current = null
    setSelected(null)
    setProjects([])
    setIssues([])
    setMyIssues([])
    setMyIssuesLoading(false)
    setMyIssuesErrors([])
    setActivityItems([])
    setActivityLoading(false)
    setActivityPartialErrors([])
    setAnalytics(null)
    setAnalyticsLoading(false)
    setAnalyticsError('')
    setAdminUsers([])
    setAdminProjects([])
    setAdminUsersLoading(false)
    setAdminUsersError('')
    setAdminUsersAfterID(null)
    setAdminUsersHasMore(false)
    setManagedUserID(null)
    setManagedUserProjectIDs([])
    setManagedUserProjectsLoading(false)
    setManagedUserProjectsError('')
    setSelectedIssue(null)
    setCreateIssueOpen(false)
    setScreen('board')
    setQuery('')
    setStatusFilter('')
    setPriorityFilter('')
    setError('')
    setToken('')
  }

  if (!token) {
    return (
      <main className="auth-shell">
        <section className="auth-promo">
          <a className="auth-brand" href="#home"><span className="brand-icon material-symbols-outlined" aria-hidden="true">view_kanban</span><span>Trackline</span></a>
          <div className="auth-promo-copy">
            <p className="eyebrow">Engineering Workspace</p>
            <h1>Issue tracking built for high-velocity engineering teams.</h1>
            <p>Keep project work and issue details in one focused workspace, with clear status, priority, and ownership at a glance.</p>
          </div>
          <div className="auth-preview-card">
            <div className="auth-preview-heading"><span>Sprint 24 Board</span><span className="preview-live"><i />32 active issues</span></div>
            <div className="auth-preview-issue"><span className="issue-id">TRK-122</span><span className="priority-badge high">High</span><strong>Optimize canvas rasterization for 60fps graph rendering</strong><small>Refactor virtualized DOM nodes within board column viewport container.</small></div>
            <div className="auth-preview-footer"><span>In progress</span><span className="preview-avatars"><i>A</i><i>M</i><i>+3</i></span></div>
          </div>
          <blockquote>“Trackline cut our sprint triage time in half. It feels as fast as a native app with zero spreadsheet overhead.”<cite>— Engineering team, Trackline customer</cite></blockquote>
          <span className="auth-promo-foot">Thoughtful work starts here.</span>
        </section>
        <section className="auth-panel">
          <div className="auth-form-wrap">
            <div className="auth-panel-heading">
              <p className="eyebrow">Your workspace</p>
              <h2>{mode === 'login' ? 'Welcome back' : mode === 'register' ? 'Create your account' : 'Forgot password?'}</h2>
              <p className="muted">{mode === 'login' ? 'Enter your credentials to access your workspaces.' : mode === 'register' ? 'Create an account to start working with your team.' : 'Password recovery is managed by your workspace administrator.'}</p>
            </div>
            {mode !== 'forgot' && <div className="auth-tabs" role="tablist" aria-label="Authentication mode">
              <button type="button" role="tab" aria-selected={mode === 'login'} className={mode === 'login' ? 'selected' : ''} onClick={() => { setMode('login'); setError('') }}>Sign in</button>
              <button type="button" role="tab" aria-selected={mode === 'register'} className={mode === 'register' ? 'selected' : ''} onClick={() => { setMode('register'); setError('') }}>Sign up</button>
            </div>}
            {mode === 'forgot' ? (
              <>
                <p className="recovery-note">Please contact your workspace administrator to reset your password.</p>
                <button className="primary-button auth-submit" onClick={() => { setMode('login'); setError('') }}>Back to sign in</button>
              </>
            ) : (
              <form onSubmit={authenticate}>
                <label>Email address<input name="email" type="email" placeholder="name@company.com" required /></label>
                <label className="password-label">
                  Password
                  {mode === 'login' && <button type="button" className="forgot-link" onClick={() => { setMode('forgot'); setError('') }}>Forgot password?</button>}
                  <span className="password-field">
                    <input name="password" type={showPassword ? 'text' : 'password'} minLength={8} placeholder="At least 8 characters" required />
                    <button type="button" className="password-toggle" aria-label={showPassword ? 'Hide password' : 'Show password'} aria-pressed={showPassword} onClick={() => setShowPassword(!showPassword)}>
                      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" /></svg>
                    </button>
                  </span>
                </label>
                <button className="primary-button auth-submit" disabled={busy}>{busy ? 'Please wait…' : mode === 'login' ? 'Sign in' : 'Create account'}<span className="material-symbols-outlined" aria-hidden="true">arrow_forward</span></button>
              </form>
            )}
            <p className="switch">
              {mode === 'register' ? 'Already have an account?' : mode === 'forgot' ? 'Remember your password?' : 'New to Trackline?'}{' '}
              <button onClick={() => { setMode(mode === 'register' || mode === 'forgot' ? 'login' : 'register'); setError('') }}>
                {mode === 'register' || mode === 'forgot' ? 'Sign in' : 'Create account'}
              </button>
            </p>
            {error && <p className="error" role="alert">{error}</p>}
          </div>
        </section>
      </main>
    )
  }

  if (mustChangePassword) {
    return <main className="password-change-page">
      <section className="password-security-panel" aria-labelledby="security-panel-title">
        <a className="auth-brand" href="#board" aria-label="Trackline home"><span className="brand-icon material-symbols-outlined" aria-hidden="true">view_kanban</span><span>Trackline</span></a>
        <div className="password-security-copy">
          <span className="security-hero-icon material-symbols-outlined" aria-hidden="true">shield_lock</span>
          <p className="eyebrow">Your account, protected</p>
          <h2 id="security-panel-title">A quick security check before you get back to work.</h2>
          <p>Choose a password only you know. Once it’s updated, you can continue to your projects and team workspace.</p>
          <ul className="security-points">
            <li><span className="material-symbols-outlined" aria-hidden="true">check_circle</span><span><strong>At least 8 characters</strong><small>Use a password that’s hard to guess.</small></span></li>
            <li><span className="material-symbols-outlined" aria-hidden="true">check_circle</span><span><strong>Keep it private</strong><small>Don’t reuse a shared or temporary password.</small></span></li>
          </ul>
        </div>
        <p className="security-panel-footer"><span className="material-symbols-outlined" aria-hidden="true">lock</span> Your password is securely protected.</p>
      </section>
      <section className="password-change-panel" aria-labelledby="password-change-title">
        <div className="password-change-card">
          <div className="password-change-heading"><span className="brand-mark material-symbols-outlined" aria-hidden="true">lock_reset</span><p className="eyebrow">Account security</p><h1 id="password-change-title">Choose a new password</h1><p className="muted">An administrator reset your password. Set a new password to continue.</p></div>
          <form onSubmit={changeRequiredPassword}>
            <div className="password-field-group"><label htmlFor="new-password">New password</label><input id="new-password" name="new_password" type="password" minLength={8} maxLength={72} autoComplete="new-password" autoFocus required aria-describedby="new-password-hint" /><small id="new-password-hint">Use at least 8 characters.</small></div>
            <div className="password-field-group"><label htmlFor="confirm-new-password">Confirm new password</label><input id="confirm-new-password" name="confirm_password" type="password" minLength={8} maxLength={72} autoComplete="new-password" required /></div>
            <button className="primary-button password-change-submit" disabled={busy}>{busy ? <><span className="material-symbols-outlined" aria-hidden="true">progress_activity</span>Updating password…</> : <>Update password<span className="material-symbols-outlined" aria-hidden="true">arrow_forward</span></>}</button>
          </form>
          {error && <p className="error password-change-error" role="alert">{error}</p>}
          <div className="password-change-signout"><span>Not your account?</span><button type="button" className="secondary-button" onClick={logout}><span className="material-symbols-outlined" aria-hidden="true">logout</span>Sign out</button></div>
        </div>
      </section>
    </main>
  }

  return (
    <>
      <ProjectBoard
        projects={projects}
        adminProjects={adminProjects}
        adminProjectsLoading={adminProjectsLoading}
        adminProjectsError={adminProjectsError}
        selectedProject={selectedProject}
        selectedProjectId={selected}
        members={members}
        projectRole={members.find(member => member.user_id === currentUserID)?.role || 'member'}
        globalRole={globalRole}
        issues={issues}
        calendarIssues={calendarIssues}
        calendarLoading={calendarLoading}
        myIssues={myIssues}
        myIssuesLoading={myIssuesLoading}
        myIssuesError={myIssuesErrors[0] || ''}
        activityItems={activityItems}
        activityLoading={activityLoading}
        activityPartialErrors={activityPartialErrors}
        analytics={analytics}
        analyticsLoading={analyticsLoading}
        analyticsError={analyticsError}
        adminUsers={adminUsers}
        adminUsersLoading={adminUsersLoading}
        adminUsersError={adminUsersError}
        adminUsersHasMore={adminUsersHasMore}
        managedUserID={managedUserID}
        managedUserProjectIDs={managedUserProjectIDs}
        managedUserProjectsLoading={managedUserProjectsLoading}
        managedUserProjectsError={managedUserProjectsError}
        currentUserID={currentUserID}
        loading={loadingIssues}
        error={error}
        query={query}
        statusFilter={statusFilter}
        priorityFilter={priorityFilter}
        email={localStorage.getItem('email') || ''}
        onSelectProject={selectProject}
        screen={screen}
        onShowChat={() => setScreen('chat')}
        onAskTrackerAssistant={askTrackerAssistant}
        onShowProjects={showProjects}
        onShowCalendar={() => setScreen('calendar')}
        onShowAdmin={() => setScreen('admin')}
        onShowMyIssues={showMyIssues}
        onShowActivity={showActivity}
        onShowAnalytics={showAnalytics}
        onShowUsers={showUsers}
        onLoadMoreUsers={() => void loadAdminUsers(adminUsersAfterID)}
        onResetUserPassword={resetUserPassword}
        onCreateUser={createAdminUser}
        onSetUserActive={setAdminUserActive}
        onManageUserProjects={manageUserProjects}
        onToggleUserProject={toggleAdminUserProject}
        onOpenBoard={() => setScreen('board')}
        onAddProject={addProjectFocus}
        onCreateProject={createProject}
        onDeleteProject={deleteProject}
        onLoadUserDirectory={loadUserDirectory}
        onAddIssue={addIssueFocus}
        onLogout={logout}
        onDismissError={() => setError('')}
        onQueryChange={setQuery}
        onStatusFilterChange={setStatusFilter}
        onPriorityFilterChange={setPriorityFilter}
        onOpenIssue={openIssue}
        onSetStatus={setStatus}
        onDeleteIssue={deleteIssue}
        onMoveIssue={moveIssue}
        onCalendarRangeChange={loadCalendarRange}
        onSearchUsers={searchUsers}
        onAddMember={addProjectMember}
        onRemoveMember={removeProjectMember}
        onSetMemberRole={setProjectMemberRole}
        movePending={movePending}
        moveToast={moveToast}
        onDismissMoveToast={() => setMoveToast('')}
      />
      {createIssueOpen && <NewIssueModal
        projects={projects}
        selectedProjectId={selected}
        members={members}
        onLoadProjectMembers={fetchProjectMembers}
        initialStatus={newIssueStatus}
        error={error}
        onClose={() => setCreateIssueOpen(false)}
        onSubmit={createIssue}
      />}
      {selectedIssue && <IssueDetailDrawer issue={selectedIssue} projectId={selectedIssue.project_id} members={members} onClose={() => setSelectedIssue(null)} onSaved={async () => {
        setSelectedIssue(null)
        await loadIssues(selectedIssue.project_id)
        if (screen === 'my-issues') await loadMyIssues()
      }} />}
    </>
  )
}

type IssueLabel = { id: number; name: string }
type ActivityEvent = { id: number; issue_id: number; actor_id: number; event_type: string; payload: Record<string, unknown>; occurred_at: string }

function IssueDetailDrawer({ issue, projectId, members, onClose, onSaved }: { issue: Issue; projectId: number; members: ProjectMember[]; onClose: () => void; onSaved: () => Promise<void> }) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [labels, setLabels] = useState<IssueLabel[]>([])
  const [activity, setActivity] = useState<ActivityEvent[]>([])
  const [activityWindowFull, setActivityWindowFull] = useState(false)
  const [activeTab, setActiveTab] = useState<'details' | 'activity'>('details')
  const [labelName, setLabelName] = useState('')
  const [savingLabel, setSavingLabel] = useState(false)
  const dialogRef = useRef<HTMLElement>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    let current = true
    api(`/issues/${issue.id}/labels`).then((result: IssueLabel[]) => {
      if (current) setLabels(result)
    }).catch(() => {
      if (current) setLabels([])
    })
    if (projectId) {
      api(`/activity?project_id=${projectId}&limit=100`).then((result: ActivityEvent[]) => {
        if (current) {
          setActivity(result.filter(item => item.issue_id === issue.id))
          setActivityWindowFull(result.length >= 100)
        }
      }).catch(() => {
        if (current) {
          setActivity([])
          setActivityWindowFull(false)
        }
      })
    }
    return () => { current = false }
  }, [issue.id, projectId])

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null
    const dialog = dialogRef.current
    const focusable = dialog?.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled])')
    focusable?.[0]?.focus()

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Tab' || !dialog) return
      const items = dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled])')
      const first = items[0]
      const last = items[items.length - 1]
      if (!first || !last) return
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      previouslyFocused?.focus()
    }
  }, [])

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const title = String(form.get('title') || '').trim()
    if (!title) {
      setError('Issue title is required.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api(`/issues/${issue.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          title,
          description: form.get('description'),
          priority: form.get('priority'),
          status: form.get('status'),
          assignee_id: String(form.get('assignee_id') || '').trim() ? Number(form.get('assignee_id')) : null,
          start_date: form.get('start_date') || null,
          due_date: form.get('due_date') || null,
        }),
      })
      await onSaved()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update issue')
    } finally {
      setBusy(false)
    }
  }

  async function addLabel() {
    const name = labelName.trim()
    if (!name) return
    setSavingLabel(true)
    setError('')
    try {
      const added = await api(`/issues/${issue.id}/labels`, {
        method: 'POST',
        body: JSON.stringify({ name }),
      }) as IssueLabel
      setLabels(current => current.some(label => label.id === added.id) ? current : [...current, added].sort((a, b) => a.name.localeCompare(b.name)))
      setLabelName('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not add label')
    } finally {
      setSavingLabel(false)
    }
  }

  return (
    <div className="dialog-backdrop drawer-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <section ref={dialogRef} className="issue-dialog issue-drawer" role="dialog" aria-modal="true" aria-labelledby="issue-dialog-title" tabIndex={-1}>
        <div className="drawer-topbar">
          <span className="drawer-breadcrumb">{projectId ? `Project ${projectId}` : 'Issue details'} <span>/</span> #{issue.id}</span>
          <div className="drawer-actions">
            <button type="button" className="icon-button" aria-label="Close issue details" onClick={onClose}><span className="material-symbols-outlined" aria-hidden="true">close</span></button>
          </div>
        </div>
        <div className="drawer-title-row">
          <span className="issue-id">TRK-{issue.id}</span>
          <span className={`priority-badge ${issue.priority}`}>{issue.priority}</span>
        </div>
        <h2 id="issue-dialog-title" className="drawer-title">{issue.title}</h2>
        <div className="drawer-tabs" role="tablist" aria-label="Issue details sections">
          <button type="button" role="tab" aria-selected={activeTab === 'details'} className={activeTab === 'details' ? 'selected' : ''} onClick={() => setActiveTab('details')}>Details</button>
          <button type="button" role="tab" aria-selected={activeTab === 'activity'} className={activeTab === 'activity' ? 'selected' : ''} onClick={() => setActiveTab('activity')}>Activity <span>{activity.length}</span></button>
        </div>
        {activeTab === 'details' ? (
          <div className="drawer-content">
            <form className="drawer-edit-form" onSubmit={save}>
              <label>Title<input name="title" defaultValue={issue.title} maxLength={240} required /></label>
              <label>Description<textarea name="description" defaultValue={issue.description} rows={6} placeholder="Add a description…" /></label>
              <div className="drawer-fields-row">
                <label>Status<select name="status" defaultValue={issue.status}><option value="todo">To do</option><option value="in_progress">In progress</option><option value="done">Done</option></select></label>
                <label>Priority<select name="priority" defaultValue={issue.priority}><option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option><option value="critical">Critical</option></select></label>
              </div>
              <label>Assignee<select name="assignee_id" defaultValue={issue.assignee_id || ''}><option value="">Unassigned</option>{members.filter(member => member.active !== false).map(member => <option key={member.user_id} value={member.user_id}>{member.username || `User #${member.user_id}`}{member.role === 'admin' ? ' · Project manager' : ''}</option>)}</select></label>
              <div className="drawer-fields-row">
                <label>Planned start<input name="start_date" type="date" defaultValue={issue.start_date || ''} /></label>
                <label>Due date<input name="due_date" type="date" defaultValue={issue.due_date || ''} min={issue.start_date || undefined} /></label>
              </div>
              <div className="issue-milestones" aria-label="Issue timestamps"><span>Assigned: {issue.assigned_at ? new Date(issue.assigned_at).toLocaleString() : 'Not assigned'}</span><span>Started: {issue.started_at ? new Date(issue.started_at).toLocaleString() : 'Not started'}</span><span>Completed: {issue.completed_at ? new Date(issue.completed_at).toLocaleString() : 'Not completed'}</span></div>
              {error && <p className="error-banner" role="alert">{error}</p>}
              <div className="dialog-actions"><button type="button" className="secondary-button" onClick={onClose}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? 'Saving…' : 'Save changes'}</button></div>
            </form>
            <div className="drawer-labels">
              <div className="drawer-section-title"><h3>Labels</h3></div>
              <div className="label-list">{labels.map(label => <span className="label-chip" key={label.id}>{label.name}</span>)}{labels.length === 0 && <span className="label-empty">No labels attached</span>}</div>
              <div className="add-label-form">
                <input aria-label="New label" maxLength={64} value={labelName} onChange={event => setLabelName(event.target.value)} placeholder="Add a label" />
                <button className="secondary-button" type="button" onClick={() => void addLabel()} disabled={savingLabel || !labelName.trim()}>{savingLabel ? 'Adding…' : 'Add label'}</button>
              </div>
            </div>
          </div>
        ) : (
          <div className="drawer-content activity-content">
            {activity.length ? activity.map(item => <article className="activity-item" key={item.id}>
              <span className="activity-avatar">{item.actor_id ? `#${item.actor_id}` : '·'}</span>
              <div><strong>{activityDescription(item)}</strong><time>{new Date(item.occurred_at).toLocaleString()}</time></div>
            </article>) : <p className="activity-empty">{activityWindowFull ? 'No activity for this issue in the latest 100 project events; older activity may exist.' : 'No activity recorded for this issue yet.'}</p>}
            {activityWindowFull && <p className="activity-limit-note">Showing activity from the latest 100 project events. Older entries may be omitted.</p>}
          </div>
        )}
        <div className="drawer-footer-note">Comments and checklist items aren’t available for this issue yet.</div>
      </section>
    </div>
  )
}

function activityDescription(item: ActivityEvent) {
  if (item.event_type === 'IssueCreated') return 'Issue created'
  if (item.event_type === 'StatusChanged' && typeof item.payload?.status === 'string') return `Status changed to ${item.payload.status.replace('_', ' ')}`
  return item.event_type.replace(/([a-z])([A-Z])/g, '$1 $2') || 'Issue updated'
}

function NewIssueModal({ projects, selectedProjectId, members: initialMembers, onLoadProjectMembers, initialStatus, error, onClose, onSubmit }: {
  projects: Project[]
  selectedProjectId: number | null
  members: ProjectMember[]
  onLoadProjectMembers: (projectID: number) => Promise<ProjectMember[]>
  initialStatus: string
  error: string
  onClose: () => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const [projectId, setProjectId] = useState(selectedProjectId ?? projects[0]?.id ?? 0)
  const [members, setMembers] = useState(initialMembers)
  const dialogRef = useRef<HTMLElement>(null)
  const onCloseRef = useRef(onClose)
  const loadMembersRef = useRef(onLoadProjectMembers)
  onCloseRef.current = onClose
  loadMembersRef.current = onLoadProjectMembers

  useEffect(() => {
    let active = true
    if (projectId) loadMembersRef.current(projectId).then(result => { if (active) setMembers(result) }).catch(() => { if (active) setMembers([]) })
    return () => { active = false }
  }, [projectId])

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null
    const dialog = dialogRef.current
    const focusable = dialog?.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled])')
    focusable?.[1]?.focus()

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Tab' || !dialog) return
      const items = dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled])')
      const first = items[0]
      const last = items[items.length - 1]
      if (!first || !last) return
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      previouslyFocused?.focus()
    }
  }, [])

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <section ref={dialogRef} className="issue-dialog new-issue-dialog" role="dialog" aria-modal="true" aria-labelledby="create-issue-title" tabIndex={-1}>
        <header className="create-issue-header">
          <div><p className="eyebrow">Trackline / Issues</p><h2 id="create-issue-title">Create new issue</h2></div>
          <button className="icon-button" type="button" aria-label="Close new issue" onClick={onClose}><span className="material-symbols-outlined" aria-hidden="true">close</span></button>
        </header>
        <form className="new-issue-form" onSubmit={onSubmit}>
          <label>Project
            <select name="project_id" aria-label="Issue project" value={projectId || ''} onChange={event => setProjectId(Number(event.target.value))} required>
              {projects.map(project => <option value={project.id} key={project.id}>{project.name}</option>)}
            </select>
          </label>
          <label>Title
            <input name="title" placeholder="Issue title..." maxLength={240} required />
          </label>
          <label>Description
            <textarea name="description" rows={6} placeholder="Add a detailed description or reproduction steps..." />
          </label>
          <div className="new-issue-fields">
            <label>Status<select name="status" defaultValue={initialStatus}><option value="todo">To do</option><option value="in_progress">In progress</option><option value="done">Done</option></select></label>
            <label>Priority<select name="priority" defaultValue="medium"><option value="critical">Urgent</option><option value="high">High</option><option value="medium">Medium</option><option value="low">Low</option></select></label>
            <label>Assignee<select name="assignee_id" defaultValue=""><option value="">Unassigned</option>{members.filter(member => member.active !== false).map(member => <option key={member.user_id} value={member.user_id}>{member.username || `User #${member.user_id}`}</option>)}</select></label>
          </div>
          <div className="new-issue-fields schedule-fields"><label>Planned start<input name="start_date" type="date" /></label><label>Due date<input name="due_date" type="date" /></label></div>
          <label>Labels <span className="optional-note">Comma-separated (applied after creation)</span>
            <input name="labels" placeholder="e.g. Security, iOS" maxLength={256} />
          </label>
          {error && <p className="error-banner" role="alert">{error}</p>}
          <footer className="new-issue-footer">
            <button className="secondary-button" type="button" onClick={onClose}>Cancel</button>
            <button className="primary-button" type="submit"><span className="material-symbols-outlined" aria-hidden="true">add</span>Create issue <kbd>⌘↵</kbd></button>
          </footer>
        </form>
      </section>
    </div>
  )
}
