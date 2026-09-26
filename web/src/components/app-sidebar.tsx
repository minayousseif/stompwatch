import { ActivityIcon, AudioLinesIcon, ListIcon, MoonIcon, ServerIcon, SunIcon } from "lucide-react"
import { Link, useLocation } from "react-router-dom"

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar"
import { useTheme } from "@/lib/theme"

/**
 * Three destinations, and no fourth. Every extra screen is a place to get
 * lost in at 2am.
 */
export const NAV = [
  { title: "Live", to: "/", icon: ActivityIcon },
  { title: "Events", to: "/events", icon: ListIcon },
  { title: "System", to: "/system", icon: ServerIcon },
] as const

/** Which nav item owns this path. /events/12 belongs to Events. */
export function activeNav(pathname: string): string {
  if (pathname.startsWith("/events")) return "/events"
  if (pathname.startsWith("/system")) return "/system"
  return "/"
}

export function AppSidebar() {
  const { isMobile, setOpenMobile } = useSidebar()
  const active = activeNav(useLocation().pathname)

  // On a phone the sidebar is a sheet over the screen, so a tap should close it.
  const close = () => {
    if (isMobile) setOpenMobile(false)
  }

  return (
    <Sidebar collapsible="offcanvas">
      <SidebarHeader className="border-b border-sidebar-border">
        <div className="flex items-center gap-2 px-2 py-1.5">
          <AudioLinesIcon className="size-5 shrink-0" aria-hidden="true" />
          <div>
            <p className="text-sm font-semibold tracking-tight">StompWatch</p>
            <p className="text-xs text-muted-foreground">Impact noise monitor</p>
          </div>
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {NAV.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton asChild tooltip={item.title} isActive={active === item.to}>
                    <Link
                      to={item.to}
                      onClick={close}
                      aria-current={active === item.to ? "page" : undefined}
                    >
                      <item.icon aria-hidden />
                      <span>{item.title}</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter>
        <ThemeToggle />
      </SidebarFooter>
    </Sidebar>
  )
}

/** Dark opens the dashboard. Daylight is the exception, so it is a small control. */
function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const next = theme === "dark" ? "light" : "dark"

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton
          onClick={() => setTheme(next)}
          tooltip={next === "light" ? "Switch to light" : "Switch to dark"}
        >
          {theme === "dark" ? <SunIcon aria-hidden /> : <MoonIcon aria-hidden />}
          <span>{next === "light" ? "Light theme" : "Dark theme"}</span>
        </SidebarMenuButton>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
