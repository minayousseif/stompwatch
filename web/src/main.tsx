import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { BrowserRouter, Route, Routes } from "react-router-dom"

import { AppShell } from "@/components/app-shell"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { EventDetailScreen } from "@/routes/event-detail"
import { EventsScreen } from "@/routes/events"
import { LiveScreen } from "@/routes/live"
import { NotFoundScreen } from "@/routes/not-found"
import { SystemScreen } from "@/routes/system"
import { ThemeProvider } from "@/lib/theme"

import "./index.css"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {/* Dark opens the interface. A light theme exists, but review happens
        at night and a white screen at 2am is unpleasant. */}
    <ThemeProvider>
      <TooltipProvider delayDuration={400}>
        <BrowserRouter>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/" element={<LiveScreen />} />
              <Route path="/events" element={<EventsScreen />} />
              <Route path="/events/:id" element={<EventDetailScreen />} />
              <Route path="/system" element={<SystemScreen />} />
              <Route path="*" element={<NotFoundScreen />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </TooltipProvider>
      <Toaster position="bottom-right" />
    </ThemeProvider>
  </StrictMode>,
)
