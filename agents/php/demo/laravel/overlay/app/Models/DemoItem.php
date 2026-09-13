<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class DemoItem extends Model
{
    protected $table = 'items';
    public $timestamps = false;
    protected $fillable = ['name', 'price'];
}
