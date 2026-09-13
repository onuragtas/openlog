<?php

namespace App\Services\Reports;

class ReportRenderer
{
    public function render(array $rows, float $total): string
    {
        $html = $this->header($total);
        foreach ($rows as $row) {
            $html .= $this->row($row);
        }
        usleep(240000);

        return $html . '</table>';
    }

    private function header(float $total): string
    {
        usleep(20000);

        return '<table data-total="' . $total . '">';
    }

    private function row(array $row): string
    {
        return '<tr><td>' . htmlspecialchars($row['user']) . '</td><td>' . $row['orders'] . '</td><td>'
            . number_format($row['sum'], 2) . '</td></tr>';
    }
}
