import type { ComponentPropsWithoutRef } from 'react'
import { cx } from './classnames'
import './ProblemList.css'

export interface Problem {
  file: string
  line: number
  message: string
}

export interface ProblemListProps extends ComponentPropsWithoutRef<'ul'> {
  problems: Problem[]
}

export function ProblemList({ problems, className, ...rest }: ProblemListProps) {
  return (
    <ul className={cx('ui-problems', className)} {...rest}>
      {problems.map((problem, index) => (
        <li key={`${problem.file}:${problem.line}:${index}`}>
          <span className="ui-problem-where">{`${problem.file}:${problem.line}`}</span>
          <span>{problem.message}</span>
        </li>
      ))}
    </ul>
  )
}
