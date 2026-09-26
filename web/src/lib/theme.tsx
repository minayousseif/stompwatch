import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react"

/**
 * Dark is what opens, because review happens at night. Light exists for
 * anyone reading the screen in daylight, and the choice is remembered.
 *
 * index.html already carries class="dark", so the first paint is dark and
 * there is nothing to flash.
 */
export type Theme = "dark" | "light"

const STORAGE_KEY = "stompwatch-theme"

const ThemeContext = createContext<{ theme: Theme; setTheme: (theme: Theme) => void }>({
  theme: "dark",
  setTheme: () => {},
})

function stored(): Theme {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY)
    return saved === "light" ? "light" : "dark"
  } catch {
    // A browser that refuses storage still gets a working dashboard.
    return "dark"
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(stored)

  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle("dark", theme === "dark")
    root.style.colorScheme = theme
  }, [theme])

  const setTheme = useCallback((next: Theme) => {
    setThemeState(next)
    try {
      window.localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // Not being able to remember the choice is not a reason to refuse it.
    }
  }, [])

  return <ThemeContext.Provider value={{ theme, setTheme }}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  return useContext(ThemeContext)
}
