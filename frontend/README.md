# Excalibase Provisioning UI

React + TypeScript + Vite frontend for the Excalibase database provisioning service.

## Features

- **Dashboard View** - List all provisioned database instances
- **Provisioning Flow Visualizer** - Real-time 8-stage pipeline visualization
- **Database Creation Form** - Interactive form to provision new databases
- **Credentials Viewer** - Secure credential display with copy-to-clipboard
- **Real-time Updates** - Auto-refresh using React Query polling
- **Dark Mode** - Matching Excalibase design system

## Tech Stack

- React 18 + TypeScript
- Vite for build tooling
- TanStack Query (React Query) for data fetching
- Tailwind CSS for styling
- Lucide React for icons
- Axios for HTTP requests

## Setup

```bash
# Install dependencies
npm install

# Start development server
npm run dev

# Build for production
npm run build

# Preview production build
npm run preview
```

The frontend will run on http://localhost:5173 and proxy API requests to http://localhost:8080

## Backend API

Make sure the Spring Boot backend is running on port 8080:

```bash
cd ..
./mvnw spring-boot:run
```

## Project Structure

```
src/
├── api/          # Axios client configuration
├── components/   # React components
│   ├── Dashboard.tsx
│   ├── DatabaseInstanceCard.tsx
│   ├── ProvisioningForm.tsx
│   ├── PipelineVisualizer.tsx
│   ├── CredentialsViewer.tsx
│   ├── Card.tsx
│   └── Button.tsx
├── hooks/        # React Query hooks
│   └── useProvisioning.ts
├── types/        # TypeScript type definitions
│   └── index.ts
├── utils/        # Utility functions
│   └── cn.ts
├── App.tsx       # Main app component
└── main.tsx      # Entry point with React Query setup
```

## API Integration

All API calls use React Query hooks from `hooks/useProvisioning.ts`:

- `useInstances()` - List all database instances (auto-refresh every 5s)
- `useInstance(projectId)` - Get single instance details (auto-refresh every 3s)
- `useCredentials(projectId)` - Get connection credentials
- `useProvisionDatabase()` - Create new database instance
- `useDeprovisionDatabase()` - Delete database instance
- `useConfigureBackup()` - Configure automated backups
- `useTriggerBackup()` - Trigger manual backup
- `useListBackups(projectId)` - List available backups

## Design System

Uses Excalibase dark mode color palette:

- Background: `#1a1d2e` (primary), `#252938` (secondary)
- Text: `#e4e6eb` (primary), `#9ca3af` (secondary)
- Accent: `#3b82f6` (blue)
- Success: `#10b981` (green)
- Warning: `#f59e0b` (orange)
- Error: `#ef4444` (red)

## Development

The app uses:

- **React Query** for server state management (NO useEffect for data fetching!)
- **Tailwind CSS** with custom color palette
- **TypeScript** for type safety
- **Vite** for fast development and HMR
