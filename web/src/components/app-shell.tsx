import { Outlet } from "react-router-dom"

import { AppSidebar } from "@/components/app-sidebar"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"

/**
 * The frame every screen sits in: the three-item sidebar on the left and the
 * screen itself on the right. A screen renders its own header, because only
 * the screen knows what range it is showing.
 */
export function AppShell() {
  return (
    <SidebarProvider>
      <AppSidebar />
      {/* min-w-0 keeps a wide child inside the frame. Without it a flex item
          is at least as wide as its content, so one oversized drawing moves
          the sidebar off the screen instead of scrolling inside its own
          section. */}
      <SidebarInset className="min-w-0">
        <Outlet />
      </SidebarInset>
    </SidebarProvider>
  )
}

/** The body of a screen: one column, the same padding everywhere. */
export function Screen({ children }: { children: React.ReactNode }) {
  return (
    // On paper the sheet's own margin is the padding, so the screen's is
    // dropped and the gaps close up.
    <main className="flex flex-col gap-4 p-3 print:gap-2 print:p-0 sm:p-4 lg:p-6">{children}</main>
  )
}
