<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class DemoOrder extends Model
{
    protected $table = 'orders';
    public $timestamps = false;
    protected $fillable = ['user_id', 'item_id', 'quantity', 'created_at'];
}
