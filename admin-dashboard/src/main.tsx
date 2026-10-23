import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { Toaster } from "@/components/ui/sonner";
import { ApiError } from "@/lib/api";
import { ThemeProvider } from "@/lib/theme";
import App from "./App";
import "./index.css";

// a 401 anywhere means the session ended: back to the sign-in page
const onError = (e: unknown) => {
  if (e instanceof ApiError && e.status === 401) queryClient.setQueryData(["me"], null);
};

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: false,
      retry: (n, e) => n < 1 && !(e instanceof ApiError && e.status < 500),
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
          <Toaster position="bottom-right" />
        </BrowserRouter>
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
);
