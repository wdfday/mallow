import { Loader2Icon } from 'lucide-react'
import { cn } from '@/lib/utils'

// Direct port of mallow-client/components/ui/spinner.tsx.
function Spinner({ className, ...props }: React.ComponentProps<'svg'>) {
  return (
    <Loader2Icon
      role="status"
      aria-label="Loading"
      className={cn('size-4 animate-spin', className)}
      {...props}
    />
  )
}

export { Spinner }
